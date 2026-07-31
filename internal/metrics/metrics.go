// Package metrics defines the Prometheus metrics exposed by the exporter.
package metrics

import "github.com/prometheus/client_golang/prometheus"

// NewGaugeVec creates the hostpath_usage_bytes gauge, labeled by hostpath
// name and path.
func NewGaugeVec() *prometheus.GaugeVec {
	return prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hostpath_usage_bytes",
		Help: "Apparent size in bytes (sum of regular file sizes) of the configured hostpath directory.",
	}, []string{"name", "path"})
}

// NewRegistry creates a fresh Prometheus registry containing only gauge,
// rather than the global default registry, so /metrics output stays minimal
// and predictable.
func NewRegistry(gauge *prometheus.GaugeVec) *prometheus.Registry {
	reg := prometheus.NewRegistry()
	reg.MustRegister(gauge)
	return reg
}
