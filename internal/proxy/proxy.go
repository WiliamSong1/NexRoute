package proxy

import (
	"adaptive-lb/internal/config"
	"adaptive-lb/internal/metrics"
	"adaptive-lb/internal/pool"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"strings"
	"sync"
	"time"
)

var errUnavailable = errors.New("no eligible backend")

type transport struct {
	base http.RoundTripper
	p    *pool.Pool
	m    *metrics.Metrics
	c    config.Config
}
type attemptKey struct{}
type outcome struct{ failed bool }
type trackedBody struct {
	io.ReadCloser
	finish func(bool)
	once   sync.Once
}

func (b *trackedBody) Read(p []byte) (int, error) {
	n, e := b.ReadCloser.Read(p)
	if e != nil {
		b.once.Do(func() { b.finish(e != io.EOF) })
	}
	return n, e
}
func (b *trackedBody) Close() error {
	e := b.ReadCloser.Close()
	b.once.Do(func() { b.finish(e != nil) })
	return e
}
func replayable(r *http.Request) bool {
	switch r.Method {
	case "GET", "HEAD", "OPTIONS", "PUT", "DELETE":
		return r.Body == nil || r.Body == http.NoBody || r.GetBody != nil
	}
	return false
}
func (t *transport) bad(code int) bool {
	for _, s := range t.c.FailureStatuses {
		if s == code {
			return true
		}
	}
	return false
}
func (t *transport) RoundTrip(r *http.Request) (*http.Response, error) {
	sent := false
	defer func() {
		if !sent && r.Body != nil {
			r.Body.Close()
		}
	}()
	started := time.Now()
	excluded := map[int]bool{}
	safe := replayable(r)
	var last error
	for attempt := 0; attempt < t.c.MaxAttempts; attempt++ {
		if e := r.Context().Err(); e != nil {
			return nil, e
		}
		remaining := t.c.RetryBudget.D() - time.Since(started)
		if remaining <= 0 {
			if last != nil {
				return nil, last
			}
			return nil, context.DeadlineExceeded
		}
		decision := time.Now()
		lease := t.p.Acquire(decision, excluded)
		t.m.Decision(time.Since(decision))
		if lease == nil {
			if last != nil {
				return nil, last
			}
			return nil, errUnavailable
		}
		excluded[lease.Index] = true
		// One header-acquisition budget across all attempts. Stop its timer once
		// final headers arrive; streaming bodies remain governed by client context.
		ctx, cancel := context.WithCancel(r.Context())
		var timer *time.Timer
		if safe {
			timer = time.AfterFunc(remaining, cancel)
		}
		stopTimer := func() {
			if timer != nil {
				timer.Stop()
			}
		}
		out := r.Clone(ctx)
		if attempt > 0 {
			if r.GetBody != nil {
				body, e := r.GetBody()
				if e != nil {
					stopTimer()
					cancel()
					lease.Finish(0, false, true, time.Now())
					return nil, e
				}
				out.Body = body
			}
			t.m.Retry()
		}
		pr := &httputil.ProxyRequest{In: r, Out: out}
		pr.SetURL(lease.URL)
		out.Host = lease.URL.Host
		// Preserve application headers. net/http may also transparently repair a
		// stale pooled connection for requests it knows are replayable.
		begin := time.Now()
		sent = true
		res, e := t.base.RoundTrip(out)
		stopTimer()
		if e != nil {
			cancel()
			lease.Finish(time.Since(begin), true, r.Context().Err() != nil, time.Now())
			last = e
			if safe && attempt+1 < t.c.MaxAttempts && r.Context().Err() == nil {
				continue
			}
			return nil, e
		}
		failed := t.bad(res.StatusCode)
		// Retrying only gateway/availability statuses avoids repeating application failures.
		retryStatus := res.StatusCode == 502 || res.StatusCode == 503 || res.StatusCode == 504
		if safe && failed && retryStatus && attempt+1 < t.c.MaxAttempts && time.Since(started) < t.c.RetryBudget.D() {
			res.Body.Close()
			cancel()
			lease.Finish(time.Since(begin), true, false, time.Now())
			last = fmt.Errorf("upstream status %d", res.StatusCode)
			continue
		}
		if o, ok := r.Context().Value(attemptKey{}).(*outcome); ok {
			o.failed = res.StatusCode >= 500
		}
		res.Body = &trackedBody{ReadCloser: res.Body, finish: func(bodyErr bool) {
			canceled := r.Context().Err() != nil
			lease.Finish(time.Since(begin), failed || bodyErr, canceled, time.Now())
			cancel()
		}}
		return res, nil
	}
	return nil, last
}

type bufferPool struct{ pool sync.Pool }

func (b *bufferPool) Get() []byte {
	if v := b.pool.Get(); v != nil {
		return v.([]byte)
	}
	return make([]byte, 32*1024)
}
func (b *bufferPool) Put(p []byte) { b.pool.Put(p) }

type Handler struct {
	rp        *httputil.ReverseProxy
	m         *metrics.Metrics
	slots     chan struct{}
	transport *http.Transport
}

func New(c config.Config, p *pool.Pool, m *metrics.Metrics) *Handler {
	tr := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: c.DialTimeout.D(), KeepAlive: 30 * time.Second}).DialContext, ForceAttemptHTTP2: true, MaxIdleConns: 1024, MaxIdleConnsPerHost: 128, MaxConnsPerHost: c.MaxInFlight, IdleConnTimeout: c.IdleTimeout.D(), TLSHandshakeTimeout: c.DialTimeout.D(), ResponseHeaderTimeout: c.HeaderTimeout.D(), ExpectContinueTimeout: time.Second, MaxResponseHeaderBytes: 1 << 20, DisableCompression: true}
	h := &Handler{m: m, slots: make(chan struct{}, c.MaxInFlight), transport: tr}
	h.rp = &httputil.ReverseProxy{Rewrite: func(pr *httputil.ProxyRequest) {
		// ReverseProxy strips Forwarded/X-Forwarded-* before Rewrite; remove all
		// remaining forwarding identity supplied by an untrusted client as well.
		for key := range pr.Out.Header {
			if strings.HasPrefix(strings.ToLower(key), "x-forwarded-") {
				pr.Out.Header.Del(key)
			}
		}
		pr.Out.Header.Del("X-Real-IP")
		pr.Out.Header.Del("Forwarded")
		pr.SetXForwarded()
	}, Transport: &transport{base: tr, p: p, m: m, c: c}, FlushInterval: -1, BufferPool: &bufferPool{}, ErrorHandler: func(w http.ResponseWriter, r *http.Request, e error) {
		if o, ok := r.Context().Value(attemptKey{}).(*outcome); ok {
			o.failed = true
		}
		if r.Context().Err() != nil {
			return
		}
		code := http.StatusBadGateway
		if errors.Is(e, errUnavailable) {
			code = http.StatusServiceUnavailable
		}
		http.Error(w, http.StatusText(code), code)
	}}
	return h
}
func (h *Handler) CloseIdleConnections() { h.transport.CloseIdleConnections() }
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	begin := time.Now()
	h.m.Begin()
	o := &outcome{}
	defer func() {
		if recovered := recover(); recovered != nil {
			h.m.End(time.Since(begin), true)
			panic(recovered)
		}
		h.m.End(time.Since(begin), o.failed)
	}()
	// Origin-form HTTP only. Upgrade tunneling is intentionally not supported.
	if r.Method == "CONNECT" || r.URL.IsAbs() || r.Header.Get("Upgrade") != "" {
		o.failed = true
		http.Error(w, "unsupported proxy request", 400)
		return
	}
	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	default:
		o.failed = true
		h.m.Reject()
		http.Error(w, "proxy capacity exhausted", 503)
		return
	}
	h.rp.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), attemptKey{}, o)))
}
