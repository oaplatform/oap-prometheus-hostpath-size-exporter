// Package metrics defines the Prometheus metrics exposed by the exporter.
package metrics

import "github.com/prometheus/client_golang/prometheus"

// Label names of hostpath_usage_bytes, in WithLabelValues order.
const (
	LabelName      = "name"
	LabelPath      = "path"
	LabelNamespace = "namespace"
	LabelPod       = "pod"
	LabelVolume    = "volume"
)

// NewGaugeVec creates the hostpath_usage_bytes gauge, labeled by hostpath
// name and path, plus the namespace/pod/volume that declared it (auto mode
// only; empty for --hostpath entries).
func NewGaugeVec() *prometheus.GaugeVec {
	return prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hostpath_usage_bytes",
		Help: "Apparent size in bytes (sum of regular file sizes) of the hostpath directory.",
	}, []string{LabelName, LabelPath, LabelNamespace, LabelPod, LabelVolume})
}

// NewRegistry creates a fresh Prometheus registry containing only gauge,
// rather than the global default registry, so /metrics output stays minimal
// and predictable.
func NewRegistry(gauge *prometheus.GaugeVec) *prometheus.Registry {
	reg := prometheus.NewRegistry()
	reg.MustRegister(gauge)
	return reg
}
