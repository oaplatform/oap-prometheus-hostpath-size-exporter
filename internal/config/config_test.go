package config

import (
	"testing"
	"time"
)

func TestParseHostpathSpec(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    Hostpath
		wantErr bool
	}{
		{
			name: "valid",
			raw:  "app:/data/app:30s",
			want: Hostpath{Name: "app", Path: "/data/app", Interval: 30 * time.Second},
		},
		{
			name: "valid with minutes",
			raw:  "cache:/var/lib/cache:5m",
			want: Hostpath{Name: "cache", Path: "/var/lib/cache", Interval: 5 * time.Minute},
		},
		{
			name:    "missing field",
			raw:     "app:/data/app",
			wantErr: true,
		},
		{
			name:    "bad duration",
			raw:     "app:/data/app:soon",
			wantErr: true,
		},
		{
			name:    "relative path",
			raw:     "app:data/app:30s",
			wantErr: true,
		},
		{
			name:    "empty name",
			raw:     ":/data/app:30s",
			wantErr: true,
		},
		{
			name:    "empty path",
			raw:     "app::30s",
			wantErr: true,
		},
		{
			name:    "zero interval",
			raw:     "app:/data/app:0s",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseHostpathSpec(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseHostpathSpec(%q) = %+v, want error", tt.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseHostpathSpec(%q) returned unexpected error: %v", tt.raw, err)
			}
			if got != tt.want {
				t.Fatalf("parseHostpathSpec(%q) = %+v, want %+v", tt.raw, got, tt.want)
			}
		})
	}
}

func TestLoad(t *testing.T) {
	cfg, err := Load([]string{
		"--hostpath", "app:/data/app:30s",
		"--hostpath", "cache:/var/lib/cache:5m",
		"--metrics_path", "/custom",
		"--metrics_port", "8080",
		"--loglevel", "debug",
	})
	if err != nil {
		t.Fatalf("Load returned unexpected error: %v", err)
	}
	if len(cfg.Hostpaths) != 2 {
		t.Fatalf("expected 2 hostpaths, got %d", len(cfg.Hostpaths))
	}
	if cfg.MetricsPath != "/custom" || cfg.MetricsPort != 8080 || cfg.LogLevel != "debug" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestLoadDuplicateName(t *testing.T) {
	_, err := Load([]string{
		"--hostpath", "app:/data/app:30s",
		"--hostpath", "app:/data/other:30s",
	})
	if err == nil {
		t.Fatal("expected error for duplicate hostpath name, got nil")
	}
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(nil)
	if err != nil {
		t.Fatalf("Load returned unexpected error: %v", err)
	}
	if cfg.MetricsPath != "/metrics" || cfg.MetricsPort != 9090 || cfg.LogLevel != "info" {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	if len(cfg.Hostpaths) != 0 {
		t.Fatalf("expected no hostpaths by default, got %d", len(cfg.Hostpaths))
	}
}

func TestLoadInvalidPort(t *testing.T) {
	_, err := Load([]string{"--metrics_port", "70000"})
	if err == nil {
		t.Fatal("expected error for out-of-range port, got nil")
	}
}
