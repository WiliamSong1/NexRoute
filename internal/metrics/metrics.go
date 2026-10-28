// Package metrics emits Prometheus text format without a runtime dependency.
package metrics

import (
	"adaptive-lb/internal/pool"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"sync"
	"time"
)

var bounds = []float64{.00001, .00005, .0001, .0005, .001, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10}

type histogram struct {
	b     [16]uint64
	count uint64
	sum   float64
}

func (h *histogram) add(v float64) {
	h.count++
	h.sum += v
	for i, b := range bounds {
		if v <= b {
			h.b[i]++
		}
	}
}

type Metrics struct {
	mu                                  sync.Mutex
	requests, errors, retries, rejected uint64
	active                              int
	duration, decision                  histogram
	algorithm                           string
	p                                   *pool.Pool
	started                             time.Time
}

func New(a string, p *pool.Pool) *Metrics { return &Metrics{algorithm: a, p: p, started: time.Now()} }
func (m *Metrics) Begin()                 { m.mu.Lock(); m.active++; m.mu.Unlock() }
func (m *Metrics) End(d time.Duration, err bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.active--
	m.requests++
	if err {
		m.errors++
	}
	m.duration.add(d.Seconds())
}
func (m *Metrics) Retry()                   { m.mu.Lock(); m.retries++; m.mu.Unlock() }
func (m *Metrics) Reject()                  { m.mu.Lock(); m.rejected++; m.mu.Unlock() }
func (m *Metrics) Decision(d time.Duration) { m.mu.Lock(); m.decision.add(d.Seconds()); m.mu.Unlock() }
func writeHistogram(w io.Writer, name string, h histogram) {
	fmt.Fprintf(w, "# TYPE %s histogram\n", name)
	for i, b := range bounds {
		fmt.Fprintf(w, "%s_bucket{le=%q} %d\n", name, fmt.Sprint(b), h.b[i])
	}
	fmt.Fprintf(w, "%s_bucket{le=\"+Inf\"} %d\n%s_sum %g\n%s_count %d\n", name, h.count, name, h.sum, name, h.count)
}
func (m *Metrics) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	m.mu.Lock()
	requests, errors, retries, rejected, active, duration, decision := m.requests, m.errors, m.retries, m.rejected, m.active, m.duration, m.decision
	m.mu.Unlock()
	fmt.Fprintf(w, "# TYPE lb_requests_total counter\nlb_requests_total{algorithm=%q} %d\n# TYPE lb_errors_total counter\nlb_errors_total %d\n# TYPE lb_retries_total counter\nlb_retries_total %d\n# TYPE lb_rejected_total counter\nlb_rejected_total %d\n# TYPE lb_active_requests gauge\nlb_active_requests %d\n", m.algorithm, requests, errors, retries, rejected, active)
	writeHistogram(w, "lb_request_duration_seconds", duration)
	writeHistogram(w, "lb_routing_duration_seconds", decision)
	for _, s := range m.p.Snapshots(time.Now()) {
		h := 0
		if s.Healthy {
			h = 1
		}
		fmt.Fprintf(w, "lb_backend_available{backend=%q} %d\nlb_backend_circuit_state{backend=%q} %d\nlb_backend_active{backend=%q} %d\nlb_backend_requests_total{backend=%q} %d\nlb_backend_errors_total{backend=%q} %d\nlb_backend_circuit_transitions_total{backend=%q} %d\nlb_backend_ewma_seconds{backend=%q} %g\n", s.ID, h, s.ID, s.State, s.ID, s.Active, s.ID, s.Requests, s.ID, s.Errors, s.ID, s.Transitions, s.ID, s.EWMA)
		for k, v := range map[string]float64{"latency": s.Score.Latency, "active": s.Score.Active, "error": s.Score.Error, "pressure": s.Score.Pressure, "total": s.Score.Total} {
			fmt.Fprintf(w, "lb_backend_score{backend=%q,component=%q} %g\n", s.ID, k, v)
		}
	}
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	fmt.Fprintf(w, "go_memstats_heap_alloc_bytes %d\ngo_goroutines %d\nprocess_start_time_seconds %g\n", mem.HeapAlloc, runtime.NumGoroutine(), float64(m.started.Unix()))
	writeCPU(w)
}
