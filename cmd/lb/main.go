package main

import (
	"adaptive-lb/internal/config"
	"adaptive-lb/internal/metrics"
	"adaptive-lb/internal/pool"
	"adaptive-lb/internal/proxy"
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	path := flag.String("config", "configs/local.json", "configuration path")
	flag.Parse()
	c, e := config.Load(*path)
	if e != nil {
		slog.Error("configuration", "error", e)
		os.Exit(1)
	}
	p := pool.New(c)
	m := metrics.New(c.Algorithm, p)
	h := proxy.New(c, p, m)
	defer h.CloseIdleConnections()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	healthCtx, cancelHealth := context.WithCancel(context.Background())
	defer cancelHealth()
	healthDone := make(chan struct{})
	go func() { p.RunHealth(healthCtx); close(healthDone) }()
	admin := http.NewServeMux()
	admin.Handle("GET /metrics", m)
	admin.HandleFunc("GET /live", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok\n")) })
	admin.HandleFunc("GET /ready", func(w http.ResponseWriter, r *http.Request) {
		if !p.Ready() {
			http.Error(w, "no ready backend", 503)
			return
		}
		w.Write([]byte("ready\n"))
	})
	server := &http.Server{Addr: c.Listen, Handler: h, ReadHeaderTimeout: c.ReadHeaderTimeout.D(), IdleTimeout: c.IdleTimeout.D(), MaxHeaderBytes: 1 << 20}
	management := &http.Server{Addr: c.Admin, Handler: admin, ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
	errs := make(chan error, 2)
	go func() { errs <- server.ListenAndServe() }()
	go func() { errs <- management.ListenAndServe() }()
	slog.Info("listening", "address", c.Listen, "admin", c.Admin, "algorithm", c.Algorithm)
	failed := false
	select {
	case <-ctx.Done():
	case e := <-errs:
		if !errors.Is(e, http.ErrServerClosed) {
			failed = true
			slog.Error("server stopped", "error", e)
		}
	}
	drain, done := context.WithTimeout(context.Background(), c.ShutdownTimeout.D())
	defer done()
	if e := server.Shutdown(drain); e != nil {
		slog.Warn("drain deadline", "error", e)
		server.Close()
	}
	management.Shutdown(drain)
	management.Close()
	cancelHealth()
	<-healthDone
	if failed {
		os.Exit(1)
	}
}
