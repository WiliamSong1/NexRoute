package pool

import (
	"adaptive-lb/internal/config"
	"adaptive-lb/internal/routing"
	"context"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"
)

type State int

const (
	Closed State = iota
	Open
	HalfOpen
)

type backend struct {
	cfg                                    config.Backend
	url                                    *url.URL
	healthy                                bool
	healthFail, healthOK, failures, active int
	state                                  State
	until                                  time.Time
	generation                             uint64
	probe                                  bool
	latency, errors                        float64
	observed, selected                     time.Time
	requests, failed, transitions          uint64
}
type Pool struct {
	mu       sync.Mutex
	cfg      config.Config
	backends []backend
	router   routing.Router
}
type Lease struct {
	pool       *Pool
	Index      int
	URL        *url.URL
	ID         string
	generation uint64
	probe      bool
	once       sync.Once
}
type Snapshot struct {
	ID                            string
	Healthy                       bool
	State                         State
	Active                        int
	Requests, Errors, Transitions uint64
	EWMA                          float64
	Score                         routing.Components
}

func New(c config.Config) *Pool {
	p := &Pool{cfg: c, router: routing.New(c.Algorithm, c.Adaptive)}
	for _, b := range c.Backends {
		u, _ := url.Parse(b.URL)
		p.backends = append(p.backends, backend{cfg: b, url: u})
	}
	return p
}
func (p *Pool) candidate(i int) routing.Candidate {
	b := &p.backends[i]
	return routing.Candidate{Index: i, Weight: b.cfg.Weight, Capacity: b.cfg.Capacity, Active: b.active, Latency: b.latency, Error: b.errors, Observed: b.observed, Selected: b.selected}
}
func (p *Pool) transition(b *backend, s State) {
	if b.state != s {
		b.state = s
		b.transitions++
		b.generation++
	}
}
func (p *Pool) Acquire(now time.Time, exclude map[int]bool) *Lease {
	p.mu.Lock()
	defer p.mu.Unlock()
	xs := make([]routing.Candidate, 0, len(p.backends))
	for i := range p.backends {
		b := &p.backends[i]
		if !b.healthy || exclude[i] || b.probe || b.active >= b.cfg.Capacity {
			continue
		}
		if b.state == Open && now.Before(b.until) {
			continue
		}
		xs = append(xs, p.candidate(i))
	}
	i := p.router.Choose(xs, now)
	if i < 0 {
		return nil
	}
	b := &p.backends[i]
	probe := b.state != Closed
	if probe {
		p.transition(b, HalfOpen)
		b.probe = true
	}
	b.active++
	b.requests++
	b.selected = now
	return &Lease{pool: p, Index: i, URL: b.url, ID: b.cfg.ID, generation: b.generation, probe: probe}
}

// Finish is idempotent and covers the full upstream body lifetime. Canceled
// client requests release capacity but do not poison backend health statistics.
func (l *Lease) Finish(d time.Duration, failed, canceled bool, now time.Time) {
	l.once.Do(func() {
		p := l.pool
		p.mu.Lock()
		defer p.mu.Unlock()
		b := &p.backends[l.Index]
		b.active--
		if l.probe {
			b.probe = false
		}
		if canceled {
			if l.probe && b.generation == l.generation {
				p.transition(b, Open)
				b.until = now.Add(p.cfg.CircuitCooldown.D())
			}
			return
		}
		a := p.cfg.Adaptive.Smoothing
		e := 0.
		if failed {
			e = 1
			b.failed++
		}
		if b.observed.IsZero() {
			b.latency = d.Seconds()
			b.errors = e
		} else {
			b.latency = a*d.Seconds() + (1-a)*b.latency
			b.errors = a*e + (1-a)*b.errors
		}
		b.observed = now
		// Old in-flight results cannot close or extend a newer circuit generation.
		if b.generation != l.generation {
			return
		}
		if failed {
			b.failures++
			if l.probe || b.failures >= p.cfg.CircuitFailures {
				p.transition(b, Open)
				b.until = now.Add(p.cfg.CircuitCooldown.D())
			}
		} else {
			b.failures = 0
			if l.probe {
				p.transition(b, Closed)
			}
		}
	})
}
func (p *Pool) HealthResult(i int, ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	b := &p.backends[i]
	if ok {
		b.healthOK++
		b.healthFail = 0
		if b.healthOK >= p.cfg.HealthSuccesses {
			b.healthy = true
		}
	} else {
		b.healthFail++
		b.healthOK = 0
		if b.healthFail >= p.cfg.HealthFailures {
			b.healthy = false
		}
	}
}
func (p *Pool) Snapshots(now time.Time) []Snapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]Snapshot, 0, len(p.backends))
	for i, b := range p.backends {
		out = append(out, Snapshot{ID: b.cfg.ID, Healthy: b.healthy, State: b.state, Active: b.active, Requests: b.requests, Errors: b.failed, Transitions: b.transitions, EWMA: b.latency, Score: routing.Score(p.candidate(i), p.cfg.Adaptive, now)})
	}
	return out
}
func (p *Pool) Ready() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, b := range p.backends {
		// Recovery-capable circuits must become ready after cooldown even if
		// an orchestrator withheld all traffic while the circuit was open.
		if b.healthy && (b.state != Open || !time.Now().Before(b.until)) {
			return true
		}
	}
	return false
}

// One serial worker per backend guarantees no overlapping checks for that backend.
func (p *Pool) RunHealth(ctx context.Context) {
	tr := &http.Transport{Proxy: nil, MaxIdleConnsPerHost: 1, IdleConnTimeout: p.cfg.IdleTimeout.D()}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: p.cfg.HealthTimeout.D(), CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	var wg sync.WaitGroup
	for i := range p.backends {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			u := *p.backends[i].url
			u.Path = p.cfg.HealthPath
			u.RawPath = ""
			u.RawQuery = ""
			timer := time.NewTimer(0)
			defer timer.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-timer.C:
					req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
					res, err := client.Do(req)
					ok := false
					if err == nil {
						ok = res.StatusCode >= 200 && res.StatusCode < 300
						_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
						res.Body.Close()
					}
					if ctx.Err() != nil {
						return
					}
					p.HealthResult(i, ok)
					timer.Reset(p.cfg.HealthInterval.D())
				}
			}
		}(i)
	}
	wg.Wait()
}
