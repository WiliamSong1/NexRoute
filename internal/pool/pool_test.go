package pool

import (
	"adaptive-lb/internal/config"
	"adaptive-lb/internal/routing"
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testPool() *Pool {
	c := config.Default()
	c.Backends = []config.Backend{{ID: "a", URL: "http://localhost:1", Weight: 1, Capacity: 1000}}
	p := New(c)
	for i := 0; i < c.HealthSuccesses; i++ {
		p.HealthResult(0, true)
	}
	return p
}
func TestCircuitRecovery(t *testing.T) {
	p := testPool()
	now := time.Now()
	for i := 0; i < p.cfg.CircuitFailures; i++ {
		l := p.Acquire(now, nil)
		l.Finish(time.Millisecond, true, false, now)
	}
	if p.Acquire(now, nil) != nil {
		t.Fatal("open circuit selected")
	}
	now = now.Add(p.cfg.CircuitCooldown.D())
	probe := p.Acquire(now, nil)
	if probe == nil || p.Acquire(now, nil) != nil {
		t.Fatal("half-open probe must be exclusive")
	}
	probe.Finish(time.Millisecond, false, false, now)
	if !p.Ready() {
		t.Fatal("not recovered")
	}
}
func TestLateSuccessCannotCloseCircuit(t *testing.T) {
	p := testPool()
	now := time.Now()
	old := p.Acquire(now, nil)
	for i := 0; i < p.cfg.CircuitFailures; i++ {
		p.Acquire(now, nil).Finish(time.Second, true, false, now)
	}
	old.Finish(time.Second, false, false, now)
	if p.Snapshots(now)[0].State != Open {
		t.Fatal("stale result closed circuit")
	}
}
func TestConcurrentLeaseAccounting(t *testing.T) {
	p := testPool()
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				l := p.Acquire(time.Now(), nil)
				if l == nil {
					t.Error("missing lease")
					return
				}
				l.Finish(time.Millisecond, false, false, time.Now())
				l.Finish(time.Second, true, false, time.Now())
			}
		}()
	}
	wg.Wait()
	s := p.Snapshots(time.Now())[0]
	if s.Active != 0 || s.Requests != 10000 || s.Errors != 0 {
		t.Fatalf("accounting %+v", s)
	}
}
func TestHealthThresholdsAndCancellation(t *testing.T) {
	p := testPool()
	p.HealthResult(0, false)
	if !p.Ready() {
		t.Fatal("one failure removed backend")
	}
	p.HealthResult(0, false)
	if p.Ready() {
		t.Fatal("failed checks ignored")
	}
	p.HealthResult(0, true)
	if p.Ready() {
		t.Fatal("restored too soon")
	}
	p.HealthResult(0, true)
	l := p.Acquire(time.Now(), nil)
	l.Finish(time.Second, true, true, time.Now())
	s := p.Snapshots(time.Now())[0]
	if s.Active != 0 || s.Errors != 0 {
		t.Fatal(s)
	}
}
func TestHealthWorkerNoOverlap(t *testing.T) {
	var active, peak atomic.Int64
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := active.Add(1)
		defer active.Add(-1)
		for {
			v := peak.Load()
			if n <= v || peak.CompareAndSwap(v, n) {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
		w.WriteHeader(200)
	}))
	defer s.Close()
	c := config.Default()
	c.HealthInterval = config.Duration(time.Millisecond)
	c.HealthSuccesses = 1
	c.Backends = []config.Backend{{ID: "a", URL: s.URL, Weight: 1, Capacity: 10}}
	p := New(c)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	p.RunHealth(ctx)
	if peak.Load() != 1 || !p.Ready() {
		t.Fatalf("overlap=%d ready=%v", peak.Load(), p.Ready())
	}
}
func TestCapacity(t *testing.T) {
	p := testPool()
	p.backends[0].cfg.Capacity = 1
	l := p.Acquire(time.Now(), nil)
	if p.Acquire(time.Now(), nil) != nil {
		t.Fatal("capacity exceeded")
	}
	l.Finish(0, false, false, time.Now())
	if p.Acquire(time.Now(), nil) == nil {
		t.Fatal("capacity not released")
	}
}

func BenchmarkPoolReservation(b *testing.B) {
	for _, name := range []string{"round_robin", "least_connections", "weighted_round_robin", "adaptive"} {
		b.Run(name, func(b *testing.B) {
			p := testPool()
			p.cfg.Algorithm = name
			p.router = routing.New(name, p.cfg.Adaptive)
			b.ReportAllocs()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					l := p.Acquire(time.Now(), nil)
					if l != nil {
						l.Finish(time.Millisecond, false, false, time.Now())
					}
				}
			})
		})
	}
}

func TestReadinessAllowsRecoveryWithoutTraffic(t *testing.T) {
	p := testPool()
	now := time.Now()
	for i := 0; i < p.cfg.CircuitFailures; i++ {
		p.Acquire(now, nil).Finish(time.Millisecond, true, false, now)
	}
	if p.Ready() {
		t.Fatal("open circuit reported ready before cooldown")
	}
	p.backends[0].until = now.Add(-time.Second)
	if !p.Ready() {
		t.Fatal("readiness would prevent recovery traffic forever")
	}
	probe := p.Acquire(time.Now(), nil)
	if probe == nil || !p.Ready() {
		t.Fatal("recovery probe unavailable")
	}
	probe.Finish(time.Millisecond, false, false, time.Now())
	if p.Snapshots(time.Now())[0].State != Closed {
		t.Fatal("probe did not close circuit")
	}
}
