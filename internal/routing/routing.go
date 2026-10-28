// Package routing contains deterministic policies. The pool serializes selection
// and reservation so least-connections observes every previous reservation.
package routing

import (
	"adaptive-lb/internal/config"
	"math"
	"time"
)

type Candidate struct {
	Index                    int
	Weight, Capacity, Active int
	Latency, Error           float64
	Observed, Selected       time.Time
}
type Components struct{ Latency, Active, Error, Pressure, Total float64 }
type Router interface {
	Choose([]Candidate, time.Time) int
}
type policy struct {
	name    string
	cfg     config.Adaptive
	cursor  uint64
	current map[int]int
	last    int
}

func New(name string, c config.Adaptive) Router {
	return &policy{name: name, cfg: c, current: make(map[int]int), last: -1}
}

// Latency and error decay toward a neutral startup prior as observations age.
// Active occupancy is normalized by capacity; pressure additionally reflects weight.
func Score(x Candidate, a config.Adaptive, now time.Time) Components {
	prior := a.LatencyReference.D().Seconds()
	lat, errRate := x.Latency, x.Error
	if x.Observed.IsZero() {
		lat = prior
		errRate = 0
	} else {
		age := math.Max(0, now.Sub(x.Observed).Seconds())
		f := math.Exp(-age / a.StaleAfter.D().Seconds())
		lat = prior + (lat-prior)*f
		errRate *= f
	}
	c := Components{Latency: lat / prior, Active: float64(x.Active) / float64(x.Capacity), Error: errRate, Pressure: float64(x.Active+1) / (float64(x.Capacity) * float64(x.Weight))}
	c.Total = a.Latency*c.Latency + a.Active*c.Active + a.Error*c.Error + a.Pressure*c.Pressure
	return c
}
func (p *policy) Choose(xs []Candidate, now time.Time) int {
	if len(xs) == 0 {
		return -1
	}
	p.cursor++
	// Rotate ties for fairness without random global state.
	start := int((p.cursor - 1) % uint64(len(xs)))
	best := start
	switch p.name {
	case "round_robin":
		return xs[start].Index
	case "least_connections":
		for k := 1; k < len(xs); k++ {
			i := (start + k) % len(xs)
			if xs[i].Active < xs[best].Active {
				best = i
			}
		}
	case "weighted_round_robin":
		eligible := map[int]bool{}
		total := 0
		for _, x := range xs {
			eligible[x.Index] = true
			p.current[x.Index] += x.Weight
			total += x.Weight
		}
		for i := range p.current {
			if !eligible[i] {
				delete(p.current, i)
			}
		}
		for k := 1; k < len(xs); k++ {
			i := (start + k) % len(xs)
			if p.current[xs[i].Index] > p.current[xs[best].Index] {
				best = i
			}
		}
		p.current[xs[best].Index] -= total
	case "adaptive":
		if p.cursor%p.cfg.ExploreEvery == 0 {
			for k := 1; k < len(xs); k++ {
				i := (start + k) % len(xs)
				if xs[i].Selected.Before(xs[best].Selected) {
					best = i
				}
			}
		} else {
			score := Score(xs[best], p.cfg, now).Total
			for k := 1; k < len(xs); k++ {
				i := (start + k) % len(xs)
				s := Score(xs[i], p.cfg, now).Total
				if s < score {
					best = i
					score = s
				}
			}
			for i, x := range xs {
				if x.Index == p.last && Score(x, p.cfg, now).Total <= score*(1+p.cfg.Hysteresis) {
					best = i
					break
				}
			}
		}
		p.last = xs[best].Index
	}
	return xs[best].Index
}
