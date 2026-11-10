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

// stringList implements flag.Value for a repeatable string flag.
type stringList []string

func (s *stringList) String() string {
	if s == nil {
		return ""
	}
	return strings.Join(*s, ",")
}

func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// Config holds fully parsed and validated exporter configuration.
type Config struct {
	Hostpaths   []Hostpath
	MetricsPath string
	MetricsPort int
	LogLevel    string

	// Auto enables auto-discovery of hostPath volumes declared by pods on the
	// exporter's own node. Mutually exclusive with Hostpaths.
	Auto bool
	// HostRoot is where the host's "/" is mounted inside the container.
	HostRoot string
	// AutoInterval is the scan interval for each discovered hostPath.
	AutoInterval time.Duration
	// AutoRefresh is how often pods on the node are re-listed.
	AutoRefresh time.Duration
	// AutoExcludes are host paths (or path prefixes) never scanned in auto mode.
	AutoExcludes []string
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
	auto := fs.Bool("auto", false, "auto-discover hostPath volumes and local PersistentVolumes of pods on this node (must run as a DaemonSet; mutually exclusive with --hostpath)")
	hostRoot := fs.String("host_root", "/host", "auto mode: where the host's / is mounted inside the container")
	autoInterval := fs.Duration("auto_interval", 60*time.Second, "auto mode: default scan interval for each discovered path (overridable by the oap/host-path-interval annotation)")
	autoRefresh := fs.Duration("auto_refresh", 30*time.Second, "auto mode: how often to re-list pods on the node")
	var autoExcludes stringList
	fs.Var(&autoExcludes, "auto_exclude", "auto mode: host path or path prefix to never scan (repeatable), e.g. /")

	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}

	if *metricsPath == "" {
		return Config{}, fmt.Errorf("--metrics_path must not be empty")
	}
	if *metricsPort <= 0 || *metricsPort > 65535 {
		return Config{}, fmt.Errorf("--metrics_port must be between 1 and 65535, got %d", *metricsPort)
	}

	if *auto && len(hostpaths) > 0 {
		return Config{}, fmt.Errorf("--auto and --hostpath are mutually exclusive")
	}
	if *autoInterval <= 0 {
		return Config{}, fmt.Errorf("--auto_interval must be positive, got %s", *autoInterval)
	}
	if *autoRefresh <= 0 {
		return Config{}, fmt.Errorf("--auto_refresh must be positive, got %s", *autoRefresh)
	}
	if !strings.HasPrefix(*hostRoot, "/") {
		return Config{}, fmt.Errorf("--host_root must be POSIX-style absolute (start with /), got %q", *hostRoot)
	}
	for _, ex := range autoExcludes {
		if !strings.HasPrefix(ex, "/") {
			return Config{}, fmt.Errorf("--auto_exclude must be POSIX-style absolute (start with /), got %q", ex)
		}
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

		Auto:         *auto,
		HostRoot:     *hostRoot,
		AutoInterval: *autoInterval,
		AutoRefresh:  *autoRefresh,
		AutoExcludes: autoExcludes,
	}, nil
}
