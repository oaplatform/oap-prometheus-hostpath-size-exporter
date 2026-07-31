// Package scanner computes directory sizes and keeps a Prometheus gauge
// updated on a per-directory refresh schedule.
package scanner

import (
	"context"
	"io/fs"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"hostpath-size-exporter/internal/config"
)

// ScanOnce walks root and returns the total apparent size (sum of regular
// file sizes) and the number of regular files found. Symlinks are not
// followed. Errors on individual entries (permission denied, races) are
// skipped rather than aborting the walk; only a failure to read root itself
// is returned as an error.
func ScanOnce(root string) (bytes int64, fileCount int, err error) {
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == root {
				return err
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		bytes += info.Size()
		fileCount++
		return nil
	})
	if walkErr != nil {
		return 0, 0, walkErr
	}
	return bytes, fileCount, nil
}

// Run scans hp.Path immediately, then on every hp.Interval tick, updating
// gauge with the result. It runs until ctx is cancelled. A scan error is
// logged and leaves the gauge at its last known value; it never stops the
// loop. Run is safe to call concurrently for different Hostpaths sharing the
// same gauge.
func Run(ctx context.Context, hp config.Hostpath, gauge *prometheus.GaugeVec, logger *slog.Logger) {
	scan(hp, gauge, logger)

	ticker := time.NewTicker(hp.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			scan(hp, gauge, logger)
		}
	}
}

func scan(hp config.Hostpath, gauge *prometheus.GaugeVec, logger *slog.Logger) {
	defer func() {
		if r := recover(); r != nil {
			logger.Error("scan panicked", "name", hp.Name, "path", hp.Path, "panic", r)
		}
	}()

	logger.Debug("scan start", "name", hp.Name, "path", hp.Path)
	start := time.Now()

	bytes, fileCount, err := ScanOnce(hp.Path)
	if err != nil {
		logger.Error("scan failed", "name", hp.Name, "path", hp.Path, "error", err)
		return
	}

	gauge.WithLabelValues(hp.Name, hp.Path).Set(float64(bytes))
	logger.Info("scan complete",
		"name", hp.Name,
		"path", hp.Path,
		"bytes", bytes,
		"files", fileCount,
		"duration", time.Since(start),
	)
}
