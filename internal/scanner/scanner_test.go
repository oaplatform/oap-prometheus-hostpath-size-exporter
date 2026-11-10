package scanner

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

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

func TestRunReportsImmediatelyAndOnTick(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.bin"), 10)

	var got atomic.Int64
	got.Store(-1)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		Run(ctx, "test", dir, 20*time.Millisecond, logger, got.Store)
		close(done)
	}()

	deadline := time.Now().Add(time.Second)
	for got.Load() != 10 {
		if time.Now().After(deadline) {
			t.Fatalf("report did not receive expected value in time, got %v", got.Load())
		}
		time.Sleep(5 * time.Millisecond)
	}

	writeFile(t, filepath.Join(dir, "b.bin"), 15)

	deadline = time.Now().Add(time.Second)
	for got.Load() != 25 {
		if time.Now().After(deadline) {
			t.Fatalf("report did not update after tick, got %v", got.Load())
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
