// Command hostpath-size-exporter exports the disk usage of one or more
// configured hostPath directories as Prometheus metrics.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"hostpath-size-exporter/internal/config"
	"hostpath-size-exporter/internal/discovery"
	"hostpath-size-exporter/internal/metrics"
	"hostpath-size-exporter/internal/scanner"
)

func main() {
	cfg, err := config.Load(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "hostpath-size-exporter:", err)
		os.Exit(2)
	}

	logger := newLogger(cfg.LogLevel)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	gauge := metrics.NewGaugeVec()
	registry := metrics.NewRegistry(gauge)

	var wg sync.WaitGroup
	if cfg.Auto {
		mgr, err := newAutoManager(ctx, cfg, gauge, logger)
		if err != nil {
			logger.Error("cannot start auto mode", "error", err)
			os.Exit(1)
		}
		logger.Info("starting hostpath-size-exporter in auto mode",
			"node", mgr.NodeName,
			"host_root", cfg.HostRoot,
			"auto_interval", cfg.AutoInterval,
			"auto_refresh", cfg.AutoRefresh,
			"auto_exclude", strings.Join(cfg.AutoExcludes, ","),
			"metrics_path", cfg.MetricsPath,
			"metrics_port", cfg.MetricsPort,
			"loglevel", cfg.LogLevel,
		)
		wg.Add(1)
		go func() {
			defer wg.Done()
			mgr.Run(ctx)
		}()
	} else {
		if len(cfg.Hostpaths) == 0 {
			logger.Warn("no --hostpath flags configured; exporter will serve an empty metric set")
		}

		names := make([]string, len(cfg.Hostpaths))
		for i, hp := range cfg.Hostpaths {
			names[i] = fmt.Sprintf("%s=%s(%s)", hp.Name, hp.Path, hp.Interval)
		}
		logger.Info("starting hostpath-size-exporter",
			"hostpaths", strings.Join(names, ", "),
			"metrics_path", cfg.MetricsPath,
			"metrics_port", cfg.MetricsPort,
			"loglevel", cfg.LogLevel,
		)

		for _, hp := range cfg.Hostpaths {
			wg.Add(1)
			go func(hp config.Hostpath) {
				defer wg.Done()
				series := gauge.WithLabelValues(hp.Name, hp.Path, "", "", "")
				scanner.Run(ctx, hp.Name, hp.Path, hp.Interval, logger, func(bytes int64) {
					series.Set(float64(bytes))
				})
			}(hp)
		}
	}

	mux := http.NewServeMux()
	mux.Handle(cfg.MetricsPath, promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.MetricsPort),
		Handler: mux,
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		logger.Info("metrics server listening", "addr", srv.Addr, "path", cfg.MetricsPath)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("metrics server failed", "error", err)
		}
	}()

	<-ctx.Done()
	logger.Info("received shutdown signal, shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("error shutting down metrics server", "error", err)
	}

	wg.Wait()
	logger.Info("shutdown complete")
}

// newAutoManager verifies the exporter runs in-cluster as a DaemonSet pod and
// builds a discovery.Manager bound to that pod's node.
func newAutoManager(ctx context.Context, cfg config.Config, gauge *prometheus.GaugeVec, logger *slog.Logger) (*discovery.Manager, error) {
	restCfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("--auto requires running inside a Kubernetes cluster: %w", err)
	}
	client, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("creating Kubernetes client: %w", err)
	}

	podName, namespace, err := discovery.SelfIdentity()
	if err != nil {
		return nil, err
	}
	checkCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	self, err := discovery.CheckDaemonSet(checkCtx, client, podName, namespace)
	if err != nil {
		return nil, err
	}

	return &discovery.Manager{
		Client:   client,
		NodeName: self.Spec.NodeName,
		SelfUID:  self.UID,
		HostRoot: cfg.HostRoot,
		Interval: cfg.AutoInterval,
		Refresh:  cfg.AutoRefresh,
		Excludes: cfg.AutoExcludes,
		Gauge:    gauge,
		Logger:   logger,
	}, nil
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "info", "":
		lvl = slog.LevelInfo
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))
	if _, ok := knownLevels[strings.ToLower(level)]; level != "" && !ok {
		logger.Warn("unknown --loglevel, defaulting to info", "loglevel", level)
	}
	return logger
}

var knownLevels = map[string]struct{}{
	"debug": {}, "info": {}, "warn": {}, "warning": {}, "error": {},
}
