// Package config parses and validates command-line configuration for the
// hostpath-size-exporter binary.
package config

import (
	"flag"
	"fmt"
	"strings"
	"time"
)

// Hostpath describes one directory to monitor: a Prometheus label name, the
// absolute filesystem path to scan, and how often to rescan it.
type Hostpath struct {
	Name     string
	Path     string
	Interval time.Duration
}

// HostpathList implements flag.Value so --hostpath can be passed multiple
// times on the command line, once per monitored directory.
type HostpathList []Hostpath

func (h *HostpathList) String() string {
	if h == nil || len(*h) == 0 {
		return ""
	}
	parts := make([]string, len(*h))
	for i, hp := range *h {
		parts[i] = fmt.Sprintf("%s:%s:%s", hp.Name, hp.Path, hp.Interval)
	}
	return strings.Join(parts, ",")
}

// Set parses a single "name:path:interval" spec and appends it to the list.
// It is called once per --hostpath occurrence by the flag package.
func (h *HostpathList) Set(raw string) error {
	hp, err := parseHostpathSpec(raw)
	if err != nil {
		return err
	}
	*h = append(*h, hp)
	return nil
}

// parseHostpathSpec parses "name:path:interval". The path must be POSIX-style
// absolute (leading "/") and must not itself contain a colon, since the spec
// is split on the first two colons only.
func parseHostpathSpec(raw string) (Hostpath, error) {
	parts := strings.SplitN(raw, ":", 3)
	if len(parts) != 3 {
		return Hostpath{}, fmt.Errorf("invalid --hostpath %q: want format name:path:interval", raw)
	}
	name, path, intervalRaw := parts[0], parts[1], parts[2]

	if name == "" {
		return Hostpath{}, fmt.Errorf("invalid --hostpath %q: name must not be empty", raw)
	}
	if path == "" {
		return Hostpath{}, fmt.Errorf("invalid --hostpath %q: path must not be empty", raw)
	}
	if !strings.HasPrefix(path, "/") {
		return Hostpath{}, fmt.Errorf("invalid --hostpath %q: path must be POSIX-style absolute (start with /)", raw)
	}
	interval, err := time.ParseDuration(intervalRaw)
	if err != nil {
		return Hostpath{}, fmt.Errorf("invalid --hostpath %q: bad interval %q: %w", raw, intervalRaw, err)
	}
	if interval <= 0 {
		return Hostpath{}, fmt.Errorf("invalid --hostpath %q: interval must be positive", raw)
	}

	return Hostpath{Name: name, Path: path, Interval: interval}, nil
}

// Config holds fully parsed and validated exporter configuration.
type Config struct {
	Hostpaths   []Hostpath
	MetricsPath string
	MetricsPort int
	LogLevel    string
}

// Load parses command-line arguments into a Config, returning an error for
// malformed flags. args should not include the program name (i.e. os.Args[1:]).
func Load(args []string) (Config, error) {
	fs := flag.NewFlagSet("hostpath-size-exporter", flag.ContinueOnError)

	var hostpaths HostpathList
	fs.Var(&hostpaths, "hostpath", "monitor a directory: name:path:interval (repeatable), e.g. app:/data/app:30s")
	metricsPath := fs.String("metrics_path", "/metrics", "HTTP path to serve Prometheus metrics on")
	metricsPort := fs.Int("metrics_port", 9090, "TCP port to serve Prometheus metrics on")
	logLevel := fs.String("loglevel", "info", "log level: debug, info, warn, or error")

	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}

	if *metricsPath == "" {
		return Config{}, fmt.Errorf("--metrics_path must not be empty")
	}
	if *metricsPort <= 0 || *metricsPort > 65535 {
		return Config{}, fmt.Errorf("--metrics_port must be between 1 and 65535, got %d", *metricsPort)
	}

	seen := make(map[string]bool, len(hostpaths))
	for _, hp := range hostpaths {
		if seen[hp.Name] {
			return Config{}, fmt.Errorf("duplicate --hostpath name %q", hp.Name)
		}
		seen[hp.Name] = true
	}

	return Config{
		Hostpaths:   hostpaths,
		MetricsPath: *metricsPath,
		MetricsPort: *metricsPort,
		LogLevel:    *logLevel,
	}, nil
}
