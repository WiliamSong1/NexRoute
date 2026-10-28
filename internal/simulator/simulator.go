package simulator

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"sync"
	"time"
)

type Config struct {
	LatencyMS        int     `json:"latency_ms"`
	JitterMS         int     `json:"jitter_ms"`
	Capacity         int     `json:"capacity"`
	ErrorProbability float64 `json:"error_probability"`
	CPUIterations    int     `json:"cpu_iterations"`
	OutageSeconds    int     `json:"outage_seconds"`
	SpikeMS          int     `json:"spike_ms"`
	SpikeSeconds     int     `json:"spike_seconds"`
}

func (c Config) Validate() error {
	if c.LatencyMS < 0 || c.LatencyMS > 60000 || c.JitterMS < 0 || c.JitterMS > 60000 || c.Capacity < 1 || c.Capacity > 100000 || c.ErrorProbability < 0 || c.ErrorProbability > 1 || math.IsNaN(c.ErrorProbability) || c.CPUIterations < 0 || c.CPUIterations > 100000000 || c.OutageSeconds < 0 || c.OutageSeconds > 3600 || c.SpikeSeconds < 0 || c.SpikeSeconds > 3600 || c.SpikeMS < 0 || c.SpikeMS > 60000 {
		return fmt.Errorf("invalid simulator parameters")
	}
	return nil
}

type Simulator struct {
	mu            sync.Mutex
	cfg           Config
	rng           *rand.Rand
	active        int
	outage, spike time.Time
	id, token     string
}

func New(id, token string, c Config, seed int64) *Simulator {
	s := &Simulator{id: id, token: token, rng: rand.New(rand.NewSource(seed))}
	s.apply(c)
	return s
}
func (s *Simulator) apply(c Config) {
	s.cfg = c
	s.outage = time.Now().Add(time.Duration(c.OutageSeconds) * time.Second)
	s.spike = time.Now().Add(time.Duration(c.SpikeSeconds) * time.Second)
}
func (s *Simulator) Control(w http.ResponseWriter, r *http.Request) {
	if s.token == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+s.token)) != 1 {
		http.Error(w, "unauthorized", 401)
		return
	}
	if r.Method != "PUT" {
		http.Error(w, "PUT required", 405)
		return
	}
	var c Config
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	d.DisallowUnknownFields()
	if e := d.Decode(&c); e != nil {
		http.Error(w, e.Error(), 400)
		return
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		http.Error(w, "one object required", 400)
		return
	}
	if e := c.Validate(); e != nil {
		http.Error(w, e.Error(), 400)
		return
	}
	s.mu.Lock()
	s.apply(c)
	s.mu.Unlock()
	w.WriteHeader(204)
}
func (s *Simulator) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	c := s.cfg
	down := time.Now().Before(s.outage)
	if r.URL.Path == "/health" {
		s.mu.Unlock()
		if down {
			http.Error(w, "outage", 503)
		} else {
			w.Write([]byte("ok"))
		}
		return
	}
	if down || s.active >= c.Capacity {
		s.mu.Unlock()
		http.Error(w, "unavailable", 503)
		return
	}
	s.active++
	delay := c.LatencyMS
	if c.JitterMS > 0 {
		delay += s.rng.Intn(2*c.JitterMS+1) - c.JitterMS
	}
	if time.Now().Before(s.spike) {
		delay += c.SpikeMS
	}
	bad := s.rng.Float64() < c.ErrorProbability
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.active--; s.mu.Unlock() }()
	timer := time.NewTimer(time.Duration(max(0, delay)) * time.Millisecond)
	defer timer.Stop()
	select {
	case <-r.Context().Done():
		return
	case <-timer.C:
	}
	hash := uint64(1)
	for i := 0; i < c.CPUIterations; i++ {
		hash = hash*1664525 + 1013904223
		if i%4096 == 0 && r.Context().Err() != nil {
			return
		}
	}
	w.Header().Set("X-Backend-ID", s.id)
	if bad {
		http.Error(w, "simulated failure", 500)
		return
	}
	if r.URL.Path == "/echo" {
		w.Header().Set("Content-Type", "application/octet-stream")
		io.Copy(w, r.Body)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"backend": s.id, "method": r.Method, "path": r.URL.Path, "query": r.URL.RawQuery, "work": hash})
}
func Shutdown(ctx context.Context, servers ...*http.Server) {
	for _, s := range servers {
		if s.Shutdown(ctx) != nil {
			s.Close()
		}
	}
}
