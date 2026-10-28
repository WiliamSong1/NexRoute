package config

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"strings"
	"time"
)

type Duration time.Duration

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	*d = Duration(v)
	return err
}
func (d Duration) D() time.Duration { return time.Duration(d) }

type Backend struct {
	ID       string `json:"id"`
	URL      string `json:"url"`
	Weight   int    `json:"weight"`
	Capacity int    `json:"capacity"`
}
type Adaptive struct {
	Smoothing        float64  `json:"smoothing"`
	LatencyReference Duration `json:"latency_reference"`
	StaleAfter       Duration `json:"stale_after"`
	ExploreEvery     uint64   `json:"explore_every"`
	Hysteresis       float64  `json:"hysteresis"`
	Latency          float64  `json:"latency"`
	Active           float64  `json:"active"`
	Error            float64  `json:"error"`
	Pressure         float64  `json:"pressure"`
}
type Config struct {
	Listen            string    `json:"listen"`
	Admin             string    `json:"admin"`
	Algorithm         string    `json:"algorithm"`
	Backends          []Backend `json:"backends"`
	DialTimeout       Duration  `json:"dial_timeout"`
	HeaderTimeout     Duration  `json:"header_timeout"`
	IdleTimeout       Duration  `json:"idle_timeout"`
	ReadHeaderTimeout Duration  `json:"read_header_timeout"`
	ShutdownTimeout   Duration  `json:"shutdown_timeout"`
	HealthInterval    Duration  `json:"health_interval"`
	HealthTimeout     Duration  `json:"health_timeout"`
	HealthPath        string    `json:"health_path"`
	HealthFailures    int       `json:"health_failures"`
	HealthSuccesses   int       `json:"health_successes"`
	CircuitFailures   int       `json:"circuit_failures"`
	CircuitCooldown   Duration  `json:"circuit_cooldown"`
	FailureStatuses   []int     `json:"failure_statuses"`
	MaxAttempts       int       `json:"max_attempts"`
	RetryBudget       Duration  `json:"retry_budget"`
	MaxInFlight       int       `json:"max_in_flight"`
	Adaptive          Adaptive  `json:"adaptive"`
}

func Default() Config {
	return Config{
		Listen: ":8080", Admin: "127.0.0.1:9090", Algorithm: "adaptive", DialTimeout: Duration(time.Second), HeaderTimeout: Duration(2 * time.Second), IdleTimeout: Duration(60 * time.Second), ReadHeaderTimeout: Duration(5 * time.Second), ShutdownTimeout: Duration(15 * time.Second),
		HealthInterval: Duration(time.Second), HealthTimeout: Duration(500 * time.Millisecond), HealthPath: "/health", HealthFailures: 2, HealthSuccesses: 2, CircuitFailures: 3, CircuitCooldown: Duration(3 * time.Second), FailureStatuses: []int{500, 502, 503, 504}, MaxAttempts: 2, RetryBudget: Duration(3 * time.Second), MaxInFlight: 4096,
		Adaptive: Adaptive{Smoothing: .2, LatencyReference: Duration(50 * time.Millisecond), StaleAfter: Duration(10 * time.Second), ExploreEvery: 20, Hysteresis: .05, Latency: 1, Active: 1, Error: 4, Pressure: 1},
	}
}
func Load(path string) (Config, error) {
	c := Default()
	f, e := os.Open(path)
	if e != nil {
		return c, e
	}
	defer f.Close()
	d := json.NewDecoder(f)
	d.DisallowUnknownFields()
	if e = d.Decode(&c); e != nil {
		return c, e
	}
	var extra any
	if e = d.Decode(&extra); e != io.EOF {
		return c, fmt.Errorf("config must contain one JSON object")
	}
	if a := os.Getenv("LB_ALGORITHM"); a != "" {
		c.Algorithm = a
	}
	return c, c.Validate()
}
func (c Config) Validate() error {
	if c.Listen == "" || c.Admin == "" || c.Listen == c.Admin {
		return fmt.Errorf("distinct listen and admin addresses required")
	}
	switch c.Algorithm {
	case "round_robin", "least_connections", "weighted_round_robin", "adaptive":
	default:
		return fmt.Errorf("unknown algorithm %q", c.Algorithm)
	}
	if len(c.Backends) == 0 || len(c.Backends) > 1024 {
		return fmt.Errorf("require 1..1024 backends")
	}
	ids := map[string]bool{}
	for _, b := range c.Backends {
		u, e := url.Parse(b.URL)
		if e != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
			return fmt.Errorf("invalid trusted backend URL %q", b.URL)
		}
		if b.ID == "" || len(b.ID) > 64 || strings.IndexFunc(b.ID, func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.')
		}) >= 0 || ids[b.ID] || b.Weight < 1 || b.Weight > 10000 || b.Capacity < 1 || b.Capacity > 1000000 {
			return fmt.Errorf("invalid backend id, weight, or capacity")
		}
		ids[b.ID] = true
	}
	for _, d := range []Duration{c.DialTimeout, c.HeaderTimeout, c.IdleTimeout, c.ReadHeaderTimeout, c.ShutdownTimeout, c.HealthInterval, c.HealthTimeout, c.CircuitCooldown, c.RetryBudget, c.Adaptive.LatencyReference, c.Adaptive.StaleAfter} {
		if d <= 0 {
			return fmt.Errorf("durations must be positive")
		}
	}
	if c.HealthFailures < 1 || c.HealthSuccesses < 1 || c.CircuitFailures < 1 || c.MaxAttempts < 1 || c.MaxAttempts > 3 || c.MaxInFlight < 1 {
		return fmt.Errorf("invalid thresholds or limits")
	}
	u, e := url.Parse(c.HealthPath)
	if e != nil || !u.IsAbs() && u.Path == "" || u.IsAbs() || u.Host != "" || len(c.HealthPath) == 0 || c.HealthPath[0] != '/' || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("health_path must be an absolute path")
	}
	a := c.Adaptive
	if a.Smoothing <= 0 || a.Smoothing > 1 || a.ExploreEvery < 2 || a.Hysteresis < 0 || a.Hysteresis >= 1 {
		return fmt.Errorf("invalid adaptive parameters")
	}
	sum := 0.
	for _, v := range []float64{a.Latency, a.Active, a.Error, a.Pressure, a.Smoothing, a.Hysteresis} {
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
			return fmt.Errorf("invalid score coefficient")
		}
	}
	sum = a.Latency + a.Active + a.Error + a.Pressure
	if sum == 0 {
		return fmt.Errorf("score coefficients cannot all be zero")
	}
	for _, s := range c.FailureStatuses {
		if s < 500 || s > 599 {
			return fmt.Errorf("passive failures must be 5xx statuses")
		}
	}
	return nil
}
