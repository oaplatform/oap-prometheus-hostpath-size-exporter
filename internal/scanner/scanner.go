// Package scanner computes directory sizes on a per-directory refresh
// schedule and reports each result to a caller-supplied callback.
package scanner

import (
	"context"
	"io/fs"
	"log/slog"
	"path/filepath"
	"time"
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

// Run scans path immediately, then on every interval tick, passing each
// successful result to report. name is used only for logging. It runs until
// ctx is cancelled. A scan error is logged and report is not called, so the
// consumer keeps its last known value; an error never stops the loop.
func Run(ctx context.Context, name, path string, interval time.Duration, logger *slog.Logger, report func(bytes int64)) {
	scan(name, path, logger, report)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			scan(name, path, logger, report)
		}
	}
}

func scan(name, path string, logger *slog.Logger, report func(bytes int64)) {
	defer func() {
		if r := recover(); r != nil {
			logger.Error("scan panicked", "name", name, "path", path, "panic", r)
		}
	}()

	logger.Debug("scan start", "name", name, "path", path)
	start := time.Now()
	bytes, fileCount, err := ScanOnce(path)
	if err != nil {
		logger.Error("scan failed", "name", name, "path", path, "error", err)
		return
	}
	report(bytes)
	logger.Info("scan complete",
		"name", name,
		"path", path,
		"bytes", bytes,
		"files", fileCount,
		"duration", time.Since(start),
	)
}
