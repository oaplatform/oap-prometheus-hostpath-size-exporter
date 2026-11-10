package discovery

import (
	"context"
	"log/slog"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"

	"hostpath-size-exporter/internal/metrics"
	"hostpath-size-exporter/internal/scanner"
)

// Manager periodically lists pods on its node and keeps exactly one scanner
// goroutine per discovered host path, publishing the result for every pod
// volume that declares that path.
type Manager struct {
	Client   kubernetes.Interface
	NodeName string
	SelfUID  types.UID
	HostRoot string
	Interval time.Duration
	Refresh  time.Duration
	Excludes []string
	Gauge    *prometheus.GaugeVec
	Logger   *slog.Logger

	targets map[string]*target
	wg      sync.WaitGroup
}

// target is one scanned host path and its current owners.
type target struct {
	cancel context.CancelFunc

	mu      sync.Mutex
	owners  []Owner
	removed bool
	scanned bool
	last    int64
}

// Run reconciles immediately, then every Refresh, until ctx is cancelled. It
// returns after all scanner goroutines it started have exited.
func (m *Manager) Run(ctx context.Context) {
	m.targets = make(map[string]*target)

	m.reconcile(ctx)

	ticker := time.NewTicker(m.Refresh)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			for _, t := range m.targets {
				t.cancel()
			}
			m.wg.Wait()
			return
		case <-ticker.C:
			m.reconcile(ctx)
		}
	}
}

// reconcile lists pods on the node and starts, updates, or stops targets so
// they match the discovered hostPaths. A List failure keeps current targets.
func (m *Manager) reconcile(ctx context.Context) {
	list, err := m.Client.CoreV1().Pods("").List(ctx, metav1.ListOptions{
		FieldSelector: fields.OneTermEqualSelector("spec.nodeName", m.NodeName).String(),
	})
	if err != nil {
		if ctx.Err() == nil {
			m.Logger.Error("listing pods on node failed; keeping current targets", "node", m.NodeName, "error", err)
		}
		return
	}

	// The field selector already restricts to this node; filter again so a
	// client that ignores it (e.g. the fake clientset) behaves the same.
	pods := slices.DeleteFunc(list.Items, func(p corev1.Pod) bool { return p.Spec.NodeName != m.NodeName })
	desired := Discover(pods, m.SelfUID, m.Excludes)

	for p, t := range m.targets {
		if _, ok := desired[p]; ok {
			continue
		}
		t.cancel()
		t.mu.Lock()
		t.removed = true
		m.Gauge.DeletePartialMatch(prometheus.Labels{metrics.LabelPath: p})
		t.mu.Unlock()
		delete(m.targets, p)
		m.Logger.Info("hostpath no longer declared, stopped scanning", "path", p)
	}

	for p, owners := range desired {
		if t, ok := m.targets[p]; ok {
			t.setOwners(m.Gauge, p, owners)
			continue
		}
		m.start(ctx, p, owners)
	}
}

func (m *Manager) start(ctx context.Context, p string, owners []Owner) {
	tctx, cancel := context.WithCancel(ctx)
	t := &target{cancel: cancel, owners: owners}
	m.targets[p] = t

	names := make([]string, len(owners))
	for i, o := range owners {
		names[i] = o.Name()
	}
	m.Logger.Info("discovered hostpath, started scanning", "path", p, "owners", names, "interval", m.Interval)

	scanPath := filepath.Join(m.HostRoot, filepath.FromSlash(p))
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		scanner.Run(tctx, p, scanPath, m.Interval, m.Logger, func(bytes int64) {
			t.report(m.Gauge, p, bytes)
		})
	}()
}

// report publishes bytes for every current owner, unless the target has been
// removed (so a scan finishing after removal cannot resurrect its series).
func (t *target) report(gauge *prometheus.GaugeVec, p string, bytes int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.removed {
		return
	}
	t.scanned = true
	t.last = bytes
	for _, o := range t.owners {
		gauge.WithLabelValues(o.Name(), p, o.Namespace, o.Pod, o.Volume).Set(float64(bytes))
	}
}

// setOwners replaces the owner list, deleting series of owners that went away
// and publishing the last known value for new owners right away.
func (t *target) setOwners(gauge *prometheus.GaugeVec, p string, owners []Owner) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, o := range t.owners {
		if !slices.Contains(owners, o) {
			gauge.DeleteLabelValues(o.Name(), p, o.Namespace, o.Pod, o.Volume)
		}
	}
	if t.scanned {
		for _, o := range owners {
			if !slices.Contains(t.owners, o) {
				gauge.WithLabelValues(o.Name(), p, o.Namespace, o.Pod, o.Volume).Set(float64(t.last))
			}
		}
	}
	t.owners = owners
}
