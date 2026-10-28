package routing

import (
	"adaptive-lb/internal/config"
	"math"
	"testing"
	"time"
)

func candidates() []Candidate {
	return []Candidate{{Index: 0, Weight: 1, Capacity: 10}, {Index: 1, Weight: 2, Capacity: 20}, {Index: 2, Weight: 3, Capacity: 30}}
}
func TestDistribution(t *testing.T) {
	for _, name := range []string{"round_robin", "weighted_round_robin"} {
		t.Run(name, func(t *testing.T) {
			p := New(name, config.Default().Adaptive)
			xs := candidates()
			counts := [3]int{}
			for i := 0; i < 600; i++ {
				counts[p.Choose(xs, time.Time{})]++
			}
			want := [3]int{200, 200, 200}
			if name == "weighted_round_robin" {
				want = [3]int{100, 200, 300}
			}
			if counts != want {
				t.Fatalf("%v != %v", counts, want)
			}
			for i := 0; i < 60; i++ {
				if p.Choose(xs[1:], time.Time{}) == 0 {
					t.Fatal("selected removed backend")
				}
			}
		})
	}
}
func TestLeastConnections(t *testing.T) {
	p := New("least_connections", config.Default().Adaptive)
	xs := candidates()
	xs[0].Active = 5
	xs[1].Active = 1
	xs[2].Active = 3
	if p.Choose(xs, time.Time{}) != 1 {
		t.Fatal("not least active")
	}
}
func TestAdaptiveExplorationAndDecay(t *testing.T) {
	a := config.Default().Adaptive
	p := New("adaptive", a)
	now := time.Unix(100, 0)
	xs := candidates()
	for i := range xs {
		xs[i].Observed = now
		xs[i].Latency = float64(i+1) * .1
	}
	counts := [3]int{}
	for i := 0; i < 600; i++ {
		j := p.Choose(xs, now)
		counts[j]++
		xs[j].Selected = now
		now = now.Add(time.Millisecond)
	}
	if counts[0] <= counts[1] || counts[1] == 0 || counts[2] == 0 {
		t.Fatalf("exploration/favoring failed %v", counts)
	}
	x := xs[2]
	x.Error = 1
	s := Score(x, a, x.Observed.Add(100*a.StaleAfter.D()))
	if math.Abs(s.Latency-1) > 1e-6 || s.Error > 1e-6 {
		t.Fatalf("stale observations did not decay %+v", s)
	}
}
func TestEmpty(t *testing.T) {
	for _, a := range []string{"round_robin", "least_connections", "weighted_round_robin", "adaptive"} {
		if New(a, config.Default().Adaptive).Choose(nil, time.Now()) != -1 {
			t.Fatal(a)
		}
	}
}
func BenchmarkRouting(b *testing.B) {
	for _, name := range []string{"round_robin", "least_connections", "weighted_round_robin", "adaptive"} {
		b.Run(name, func(b *testing.B) {
			p := New(name, config.Default().Adaptive)
			xs := candidates()
			now := time.Now()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				p.Choose(xs, now)
			}
		})
	}
}

func TestAdaptiveHysteresisAndScoreDimensions(t *testing.T) {
	a := config.Default().Adaptive
	now := time.Now()
	p := New("adaptive", a)
	xs := []Candidate{{Index: 0, Weight: 1, Capacity: 10, Latency: .04, Observed: now}, {Index: 1, Weight: 1, Capacity: 10, Latency: .05, Observed: now}}
	if p.Choose(xs, now) != 0 {
		t.Fatal("startup preference")
	}
	xs[0].Latency = .051
	if p.Choose(xs, now) != 0 {
		t.Fatal("small score change oscillated")
	}
	xs[0].Error = 1
	if p.Choose(xs, now) != 1 {
		t.Fatal("error penalty ignored")
	}
	x := xs[1]
	base := Score(x, a, now)
	x.Active = 5
	busy := Score(x, a, now)
	if busy.Total <= base.Total || busy.Active <= base.Active || busy.Pressure <= base.Pressure {
		t.Fatal("load dimensions not monotonic")
	}
	x.Weight = 3
	if Score(x, a, now).Pressure >= busy.Pressure {
		t.Fatal("weight has no effect")
	}
}
