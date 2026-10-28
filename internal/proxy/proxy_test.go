package proxy

import (
	"adaptive-lb/internal/config"
	"adaptive-lb/internal/metrics"
	"adaptive-lb/internal/pool"
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func setup(t *testing.T, urls ...string) (*Handler, *pool.Pool, *metrics.Metrics) {
	t.Helper()
	c := config.Default()
	c.Algorithm = "round_robin"
	for i, u := range urls {
		c.Backends = append(c.Backends, config.Backend{ID: fmt.Sprint(i), URL: u, Weight: 1, Capacity: 100})
	}
	p := pool.New(c)
	for i := range urls {
		p.HealthResult(i, true)
		p.HealthResult(i, true)
	}
	m := metrics.New(c.Algorithm, p)
	h := New(c, p, m)
	t.Cleanup(h.CloseIdleConnections)
	return h, p, m
}
func TestForwardingAndSanitization(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/base/echo" || r.URL.RawQuery != "x=1&x=2" {
			t.Errorf("wrong request %s %s", r.Method, r.URL)
		}
		if strings.Contains(r.Header.Get("X-Forwarded-For"), "spoof") || r.Header.Get("Forwarded") != "" || r.Header.Get("X-Real-IP") != "" || r.Header.Get("X-Remove") != "" {
			t.Error("untrusted forwarding or hop headers leaked")
		}
		if r.Header.Get("X-Custom") != "preserve" {
			t.Error("lost header")
		}
		w.Header().Set("X-Upstream", "yes")
		w.WriteHeader(201)
		io.Copy(w, r.Body)
	}))
	defer up.Close()
	h, _, _ := setup(t, up.URL+"/base")
	req := httptest.NewRequest("POST", "/echo?x=1&x=2", strings.NewReader("payload"))
	req.Header.Set("X-Forwarded-For", "spoof")
	req.Header.Set("Forwarded", "for=spoof")
	req.Header.Set("X-Real-IP", "spoof")
	req.Header.Set("Connection", "X-Remove")
	req.Header.Set("X-Remove", "secret")
	req.Header.Set("X-Custom", "preserve")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 201 || w.Body.String() != "payload" || w.Header().Get("X-Upstream") != "yes" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}
func TestRetrySafety(t *testing.T) {
	for _, method := range []string{"GET", "POST", "PUT"} {
		t.Run(method, func(t *testing.T) {
			var calls atomic.Int64
			bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); http.Error(w, "bad", 503) }))
			defer bad.Close()
			good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.Write([]byte("ok")) }))
			defer good.Close()
			h, p, _ := setup(t, bad.URL, good.URL)
			var body io.Reader
			if method != "GET" {
				body = strings.NewReader("non-replayable")
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(method, "/", body))
			want := int64(1)
			code := 503
			if method == "GET" {
				want = 2
				code = 200
			}
			if calls.Load() != want || w.Code != code {
				t.Fatalf("calls=%d code=%d", calls.Load(), w.Code)
			}
			for _, s := range p.Snapshots(time.Now()) {
				if s.Active != 0 {
					t.Fatal("leaked active")
				}
			}
		})
	}
}
func TestReplayablePUT(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.Copy(io.Discard, r.Body); w.WriteHeader(503) }))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.Copy(w, r.Body) }))
	defer good.Close()
	h, _, _ := setup(t, bad.URL, good.URL)
	r := httptest.NewRequest("PUT", "/", strings.NewReader("replay"))
	r.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("replay")), nil }
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || w.Body.String() != "replay" {
		t.Fatal(w.Code, w.Body)
	}
}
func TestNoBackendsAndAbsoluteURL(t *testing.T) {
	h, p, _ := setup(t, "http://127.0.0.1:1")
	p.HealthResult(0, false)
	p.HealthResult(0, false)
	for _, tc := range []struct {
		url  string
		code int
	}{{"/", 503}, {"http://evil.example/", 400}} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", tc.url, nil))
		if w.Code != tc.code {
			t.Fatal(w.Code)
		}
	}
}
func TestStreamingAndCancellation(t *testing.T) {
	done := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("first\n"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(done)
	}))
	defer up.Close()
	h, p, _ := setup(t, up.URL)
	s := httptest.NewServer(h)
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r, _ := http.NewRequestWithContext(ctx, "GET", s.URL, nil)
	res, e := http.DefaultClient.Do(r)
	if e != nil {
		t.Fatal(e)
	}
	line, e := bufio.NewReader(res.Body).ReadString('\n')
	if e != nil || line != "first\n" {
		t.Fatal(line, e)
	}
	if p.Snapshots(time.Now())[0].Active != 1 {
		t.Fatal("released before streaming body completed")
	}
	cancel()
	res.Body.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("upstream cancellation not propagated")
	}
	deadline := time.Now().Add(time.Second)
	for p.Snapshots(time.Now())[0].Active != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	snp := p.Snapshots(time.Now())[0]
	if snp.Active != 0 || snp.Errors != 0 {
		t.Fatal(snp)
	}
}
func TestRejectAmbiguousFraming(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("ambiguous request reached upstream") }))
	defer up.Close()
	h, _, _ := setup(t, up.URL)
	s := httptest.NewServer(h)
	defer s.Close()
	conn, e := net.Dial("tcp", strings.TrimPrefix(s.URL, "http://"))
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(time.Second))
	fmt.Fprint(conn, "POST / HTTP/1.1\r\nHost: test\r\nContent-Length: 1\r\nContent-Length: 2\r\n\r\nx")
	res, e := http.ReadResponse(bufio.NewReader(conn), nil)
	if e != nil {
		t.Fatal(e)
	}
	res.Body.Close()
	if res.StatusCode != 400 {
		t.Fatal(res.StatusCode)
	}
}
func TestMetrics(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) }))
	defer up.Close()
	h, _, m := setup(t, up.URL)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
	w := httptest.NewRecorder()
	m.ServeHTTP(w, httptest.NewRequest("GET", "/metrics", nil))
	for _, v := range []string{"lb_requests_total{algorithm=\"round_robin\"} 1", "lb_request_duration_seconds_count 1", "lb_backend_active{backend=\"0\"} 0"} {
		if !strings.Contains(w.Body.String(), v) {
			t.Fatal("missing", v)
		}
	}
}

func TestApplicationErrorIsNotBackendFailure(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) }))
	defer up.Close()
	h, p, _ := setup(t, up.URL)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/missing", nil))
	if w.Code != 404 || p.Snapshots(time.Now())[0].Errors != 0 {
		t.Fatal("application error poisoned health")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestRetryBudgetAndTransportFailure(t *testing.T) {
	for _, slow := range []bool{false, true} {
		t.Run(fmt.Sprint(slow), func(t *testing.T) {
			c := config.Default()
			c.RetryBudget = config.Duration(30 * time.Millisecond)
			c.Algorithm = "round_robin"
			c.Backends = []config.Backend{{ID: "a", URL: "http://localhost:1", Weight: 1, Capacity: 10}, {ID: "b", URL: "http://localhost:2", Weight: 1, Capacity: 10}}
			p := pool.New(c)
			for i := 0; i < 2; i++ {
				p.HealthResult(i, true)
				p.HealthResult(i, true)
			}
			m := metrics.New(c.Algorithm, p)
			calls := 0
			tr := &transport{c: c, p: p, m: m, base: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if slow {
					<-r.Context().Done()
					return nil, r.Context().Err()
				}
				if calls == 1 {
					return nil, fmt.Errorf("connect failed")
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ok")), Header: make(http.Header)}, nil
			})}
			r := httptest.NewRequest("GET", "/", nil)
			begin := time.Now()
			res, err := tr.RoundTrip(r)
			if slow {
				if err == nil || calls != 1 || time.Since(begin) > time.Second {
					t.Fatalf("budget err=%v calls=%d", err, calls)
				}
			} else {
				if err != nil || calls != 2 {
					t.Fatalf("retry err=%v calls=%d", err, calls)
				}
				io.Copy(io.Discard, res.Body)
				res.Body.Close()
			}
			for _, s := range p.Snapshots(time.Now()) {
				if s.Active != 0 {
					t.Fatal("capacity leaked")
				}
			}
		})
	}
}
func TestNoRetryAfterCommittedResponse(t *testing.T) {
	var calls atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		conn, rw, e := w.(http.Hijacker).Hijack()
		if e != nil {
			t.Error(e)
			return
		}
		fmt.Fprint(rw, "HTTP/1.1 200 OK\r\nContent-Length: 100\r\n\r\npartial")
		rw.Flush()
		conn.Close()
	}))
	defer up.Close()
	h, p, _ := setup(t, up.URL, up.URL)
	s := httptest.NewServer(h)
	defer s.Close()
	res, e := http.Get(s.URL)
	if e == nil {
		_, _ = io.ReadAll(res.Body)
		res.Body.Close()
	}
	if calls.Load() != 1 {
		t.Fatal("retried committed response")
	}
	var active, failed uint64
	for _, b := range p.Snapshots(time.Now()) {
		active += uint64(b.Active)
		failed += b.Errors
	}
	if active != 0 || failed != 1 {
		t.Fatalf("active=%d errors=%d", active, failed)
	}
}
func TestStreamingOutlivesRetryBudget(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("a"))
		w.(http.Flusher).Flush()
		time.Sleep(80 * time.Millisecond)
		w.Write([]byte("b"))
	}))
	defer up.Close()
	c := config.Default()
	c.RetryBudget = config.Duration(20 * time.Millisecond)
	c.Backends = []config.Backend{{ID: "a", URL: up.URL, Weight: 1, Capacity: 10}}
	p := pool.New(c)
	p.HealthResult(0, true)
	p.HealthResult(0, true)
	m := metrics.New(c.Algorithm, p)
	h := New(c, p, m)
	defer h.CloseIdleConnections()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Body.String() != "ab" {
		t.Fatal("header budget cut off stream", w.Body.String())
	}
}

func TestNonReplayableUploadOutlivesRetryBudget(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		w.Write(body)
	}))
	defer up.Close()
	c := config.Default()
	c.RetryBudget = config.Duration(20 * time.Millisecond)
	c.Backends = []config.Backend{{ID: "a", URL: up.URL, Weight: 1, Capacity: 10}}
	p := pool.New(c)
	p.HealthResult(0, true)
	p.HealthResult(0, true)
	m := metrics.New(c.Algorithm, p)
	h := New(c, p, m)
	defer h.CloseIdleConnections()
	reader, writer := io.Pipe()
	go func() {
		defer writer.Close()
		writer.Write([]byte("a"))
		time.Sleep(80 * time.Millisecond)
		writer.Write([]byte("b"))
	}()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/", reader))
	if w.Code != 200 || w.Body.String() != "ab" {
		t.Fatal(w.Code, w.Body.String())
	}
}
