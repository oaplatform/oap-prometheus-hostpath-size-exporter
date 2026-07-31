package scanner

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"hostpath-size-exporter/internal/config"
)

func testGaugeVec() *prometheus.GaugeVec {
	return prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "test_hostpath_usage_bytes",
		Help: "test",
	}, []string{"name", "path"})
}

func gaugeValue(t *testing.T, gauge *prometheus.GaugeVec, name, path string) float64 {
	t.Helper()
	m := &dto.Metric{}
	if err := gauge.WithLabelValues(name, path).Write(m); err != nil {
		t.Fatalf("failed to read gauge value: %v", err)
	}
	return m.GetGauge().GetValue()
}

func TestScanOnce(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.bin"), 10)
	writeFile(t, filepath.Join(dir, "b.bin"), 20)
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(sub, "c.bin"), 5)

	bytes, fileCount, err := ScanOnce(dir)
	if err != nil {
		t.Fatalf("ScanOnce returned unexpected error: %v", err)
	}
	if bytes != 35 {
		t.Fatalf("expected 35 bytes, got %d", bytes)
	}
	if fileCount != 3 {
		t.Fatalf("expected 3 files, got %d", fileCount)
	}
}

func TestScanOnceMissingPath(t *testing.T) {
	_, _, err := ScanOnce(filepath.Join(t.TempDir(), "does-not-exist"))
	if err == nil {
		t.Fatal("expected error for missing path, got nil")
	}
}

func TestRunUpdatesGaugeImmediatelyAndOnTick(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.bin"), 10)

	gauge := testGaugeVec()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	hp := config.Hostpath{Name: "test", Path: dir, Interval: 20 * time.Millisecond}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		Run(ctx, hp, gauge, logger)
		close(done)
	}()

	deadline := time.Now().Add(time.Second)
	for gaugeValue(t, gauge, "test", dir) != 10 {
		if time.Now().After(deadline) {
			t.Fatalf("gauge did not reach expected value in time, got %v", gaugeValue(t, gauge, "test", dir))
		}
		time.Sleep(5 * time.Millisecond)
	}

	writeFile(t, filepath.Join(dir, "b.bin"), 15)

	deadline = time.Now().Add(time.Second)
	for gaugeValue(t, gauge, "test", dir) != 25 {
		if time.Now().After(deadline) {
			t.Fatalf("gauge did not update after tick, got %v", gaugeValue(t, gauge, "test", dir))
		}
		time.Sleep(5 * time.Millisecond)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return after context cancellation")
	}
}

func writeFile(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
}
