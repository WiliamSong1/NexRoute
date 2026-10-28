package config

import "testing"

func TestValidation(t *testing.T) {
	base := Default()
	base.Backends = []Backend{{ID: "a", URL: "http://localhost:8081", Weight: 1, Capacity: 10}}
	if e := base.Validate(); e != nil {
		t.Fatal(e)
	}
	for _, mutate := range []func(*Config){func(c *Config) { c.Backends[0].URL = "file:///etc/passwd" }, func(c *Config) { c.Backends[0].Weight = 0 }, func(c *Config) { c.Adaptive.Smoothing = 2 }, func(c *Config) { c.Algorithm = "unknown" }, func(c *Config) { c.MaxAttempts = 100 }, func(c *Config) { c.FailureStatuses = []int{404} }, func(c *Config) { c.HealthPath = "https://elsewhere/health" }} {
		c := base
		c.Backends = append([]Backend(nil), base.Backends...)
		mutate(&c)
		if c.Validate() == nil {
			t.Fatal("invalid configuration accepted")
		}
	}
}

func TestMetricLabelIDs(t *testing.T) {
	for _, id := range []string{"has\ttab", "has\nnewline", "has\"quote"} {
		c := Default()
		c.Backends = []Backend{{ID: id, URL: "http://localhost", Weight: 1, Capacity: 1}}
		if c.Validate() == nil {
			t.Fatalf("invalid label ID accepted: %q", id)
		}
	}
}
