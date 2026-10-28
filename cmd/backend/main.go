package main

import (
	"adaptive-lb/internal/simulator"
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	id := flag.String("id", "a", "backend ID")
	listen := flag.String("listen", ":8081", "data address")
	admin := flag.String("admin", "127.0.0.1:9081", "control address")
	latency := flag.Int("latency", 10, "milliseconds")
	jitter := flag.Int("jitter", 2, "milliseconds")
	capacity := flag.Int("capacity", 16, "max concurrent requests")
	seed := flag.Int64("seed", 1, "random seed")
	flag.Parse()
	c := simulator.Config{LatencyMS: *latency, JitterMS: *jitter, Capacity: *capacity}
	if e := c.Validate(); e != nil {
		slog.Error("config", "error", e)
		os.Exit(1)
	}
	sim := simulator.New(*id, os.Getenv("SIM_TOKEN"), c, *seed)
	mux := http.NewServeMux()
	mux.HandleFunc("/control", sim.Control)
	data := &http.Server{Addr: *listen, Handler: sim, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
	control := &http.Server{Addr: *admin, Handler: mux, ReadHeaderTimeout: 3 * time.Second, WriteTimeout: 3 * time.Second, IdleTimeout: 30 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errs := make(chan error, 2)
	go func() { errs <- data.ListenAndServe() }()
	go func() { errs <- control.ListenAndServe() }()
	failed := false
	select {
	case <-ctx.Done():
	case e := <-errs:
		failed = true
		slog.Error("server stopped", "error", e)
	}
	drain, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	simulator.Shutdown(drain, data, control)
	if failed {
		os.Exit(1)
	}
}
