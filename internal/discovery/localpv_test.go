package discovery

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"hostpath-size-exporter/internal/metrics"
)

func pvcVol(name, claim string) corev1.Volume {
	return corev1.Volume{Name: name, VolumeSource: corev1.VolumeSource{
		PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: claim},
	}}
}

func ephemeralVol(name string) corev1.Volume {
	return corev1.Volume{Name: name, VolumeSource: corev1.VolumeSource{
		Ephemeral: &corev1.EphemeralVolumeSource{},
	}}
}

func annotated(pod corev1.Pod, ann map[string]string) corev1.Pod {
	pod.Annotations = ann
	return pod
}

func TestParseSettings(t *testing.T) {
	for _, tc := range []struct {
		name  string
		ann   map[string]string
		want  settings
		warns int
	}{
		{"none", nil, settings{}, 0},
		{"interval", map[string]string{AnnotationInterval: "30s"}, settings{interval: 30 * time.Second}, 0},
		{"refresh alias", map[string]string{AnnotationRefresh: "5m"}, settings{interval: 5 * time.Minute}, 0},
		{"interval wins", map[string]string{AnnotationInterval: "30s", AnnotationRefresh: "5m"}, settings{interval: 30 * time.Second}, 0},
		{"invalid interval falls back to refresh", map[string]string{AnnotationInterval: "soon", AnnotationRefresh: "5m"}, settings{interval: 5 * time.Minute}, 1},
		{"non-positive interval", map[string]string{AnnotationInterval: "0s"}, settings{}, 1},
		{"exclude", map[string]string{AnnotationExclude: "true"}, settings{exclude: true, excludeSet: true}, 0},
		{"exclude false", map[string]string{AnnotationExclude: "false"}, settings{excludeSet: true}, 0},
		{"invalid exclude", map[string]string{AnnotationExclude: "maybe"}, settings{}, 1},
	} {
		warns := 0
		got := parseSettings(tc.ann, func(string, string, error) { warns++ })
		if got != tc.want || warns != tc.warns {
			t.Errorf("%s: got %+v (%d warnings), want %+v (%d warnings)", tc.name, got, warns, tc.want, tc.warns)
		}
	}
}

func TestMerge(t *testing.T) {
	pv := settings{interval: time.Minute}
	pod := settings{interval: time.Hour, exclude: true, excludeSet: true}
	if got, want := merge(pv, pod), (settings{interval: time.Minute, exclude: true, excludeSet: true}); got != want {
		t.Fatalf("merge = %+v, want %+v", got, want)
	}
	pv = settings{excludeSet: true}
	if got, want := merge(pv, pod), (settings{interval: time.Hour, excludeSet: true}); got != want {
		t.Fatalf("merge = %+v, want %+v", got, want)
	}
}

func TestClaims(t *testing.T) {
	pods := []corev1.Pod{
		podWithVolumes("monitoring", "exporter", corev1.PodRunning, pvcVol("x", "self-claim")),
		podWithVolumes("b", "app", corev1.PodRunning, pvcVol("data", "data"), ephemeralVol("scratch"), hostVol("h", "/h", nil)),
		podWithVolumes("a", "app", corev1.PodPending, pvcVol("data", "shared"), pvcVol("data2", "shared")),
		podWithVolumes("a", "done", corev1.PodFailed, pvcVol("data", "old")),
	}
	got := Claims(pods, types.UID("monitoring/exporter"))
	want := []ClaimKey{{"a", "shared"}, {"b", "app-scratch"}, {"b", "data"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Claims = %+v, want %+v", got, want)
	}
}

func TestDiscoverLocalPVAndAnnotations(t *testing.T) {
	pods := []corev1.Pod{
		annotated(podWithVolumes("a", "web", corev1.PodRunning,
			pvcVol("data", "data-web"),
			hostVol("cache", "/mnt/disk1/", nil),
		), map[string]string{AnnotationInterval: "10s"}),
		annotated(podWithVolumes("d", "slow", corev1.PodRunning, hostVol("h", "/mnt/disk1", nil)),
			map[string]string{AnnotationInterval: "1h"}),
		annotated(podWithVolumes("a", "job", corev1.PodRunning, ephemeralVol("scratch")),
			map[string]string{AnnotationInterval: "1s"}),
		podWithVolumes("b", "api", corev1.PodRunning, pvcVol("data", "api-data")),
		annotated(podWithVolumes("b", "skip", corev1.PodRunning,
			hostVol("h", "/mnt/disk1", nil),
			pvcVol("local", "skip-local"),
		), map[string]string{AnnotationExclude: "true"}),
		podWithVolumes("c", "unresolved", corev1.PodRunning, pvcVol("x", "nope")),
		annotated(podWithVolumes("c", "bad", corev1.PodRunning, hostVol("h", "/mnt/disk5", nil)),
			map[string]string{AnnotationInterval: "fast"}),
		podWithVolumes("c", "excl", corev1.PodRunning, pvcVol("logs", "logs")),
	}
	localPVs := map[ClaimKey]LocalPV{
		{"a", "data-web"}:    {Name: "pv1", Path: "/mnt/disk1"},
		{"a", "job-scratch"}: {Name: "pv2", Path: "/mnt/disk2", Annotations: map[string]string{AnnotationInterval: "5m"}},
		{"b", "api-data"}:    {Name: "pv3", Path: "/mnt/disk3", Annotations: map[string]string{AnnotationExclude: "true"}},
		{"b", "skip-local"}:  {Name: "pv4", Path: "/mnt/disk4", Annotations: map[string]string{AnnotationExclude: "false"}},
		{"c", "logs"}:        {Name: "pv6", Path: "/var/log/x"},
	}

	type warning struct{ object, key, value string }
	var warnings []warning
	got := Discover(pods, "", []string{"/var/log"}, localPVs, time.Minute, func(object, key, value string, _ error) {
		warnings = append(warnings, warning{object, key, value})
	})

	want := map[string]Desired{
		// hostPath and local PV owners merge; min interval wins; the
		// pod-excluded b/skip/h is dropped.
		"/mnt/disk1": {Interval: 10 * time.Second, Owners: []Owner{
			{Namespace: "a", Pod: "web", Volume: "cache"},
			{Namespace: "a", Pod: "web", Volume: "data"},
			{Namespace: "d", Pod: "slow", Volume: "h"},
		}},
		// PV annotation beats the pod annotation.
		"/mnt/disk2": {Interval: 5 * time.Minute, Owners: []Owner{{Namespace: "a", Pod: "job", Volume: "scratch"}}},
		// PV exclude=false beats pod exclude=true.
		"/mnt/disk4": {Interval: time.Minute, Owners: []Owner{{Namespace: "b", Pod: "skip", Volume: "local"}}},
		// Invalid annotation falls back to the default interval.
		"/mnt/disk5": {Interval: time.Minute, Owners: []Owner{{Namespace: "c", Pod: "bad", Volume: "h"}}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Discover = %+v, want %+v", got, want)
	}
	if want := []warning{{"pod c/bad", AnnotationInterval, "fast"}}; !reflect.DeepEqual(warnings, want) {
		t.Fatalf("warnings = %+v, want %+v", warnings, want)
	}
}

func pvc(ns, name, volumeName string) *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec:       corev1.PersistentVolumeClaimSpec{VolumeName: volumeName},
	}
}

func pv(name string, source corev1.PersistentVolumeSource, mode corev1.PersistentVolumeMode) *corev1.PersistentVolume {
	return &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       corev1.PersistentVolumeSpec{PersistentVolumeSource: source, VolumeMode: &mode},
	}
}

func localSource(p string) corev1.PersistentVolumeSource {
	return corev1.PersistentVolumeSource{Local: &corev1.LocalVolumeSource{Path: p}}
}

func writeFile(t *testing.T, root, dir string, size int) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(dir))
	if err := os.MkdirAll(full, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(full, "f.bin"), make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newTestManager(t *testing.T, client *fake.Clientset, hostRoot string) (*Manager, *prometheus.GaugeVec, context.Context) {
	t.Helper()
	gauge := metrics.NewGaugeVec()
	m := &Manager{
		Client:   client,
		NodeName: "node-1",
		HostRoot: hostRoot,
		Interval: time.Hour,
		Refresh:  time.Hour,
		Gauge:    gauge,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		targets:  make(map[string]*target),
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		for _, tg := range m.targets {
			tg.cancel()
		}
		m.wg.Wait()
	})
	return m, gauge, ctx
}

func waitValue(t *testing.T, gauge *prometheus.GaugeVec, series int, p string, o Owner, want float64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		n := testutil.CollectAndCount(gauge)
		if n == series {
			if v := testutil.ToFloat64(gauge.WithLabelValues(o.Name(), p, o.Namespace, o.Pod, o.Volume)); v == want {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected %d series and %s %s = %v; got %d series", series, p, o.Name(), want, n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestManagerLocalPV(t *testing.T) {
	hostRoot := t.TempDir()
	writeFile(t, hostRoot, "/mnt/disk1", 7)
	writeFile(t, hostRoot, "/mnt/other", 3)

	db := podWithVolumes("ns", "db", corev1.PodRunning,
		pvcVol("data", "data-db"),
		pvcVol("nfs", "nfs-claim"),
		pvcVol("raw", "raw-claim"),
		pvcVol("gone", "missing"),
		pvcVol("pending", "unbound"),
	)
	client := fake.NewSimpleClientset(
		&db,
		pvc("ns", "data-db", "pv-local"),
		pvc("ns", "nfs-claim", "pv-nfs"),
		pvc("ns", "raw-claim", "pv-block"),
		pvc("ns", "unbound", ""),
		pv("pv-local", localSource("/mnt/disk1"), corev1.PersistentVolumeFilesystem),
		pv("pv-nfs", corev1.PersistentVolumeSource{NFS: &corev1.NFSVolumeSource{Server: "nfs", Path: "/export"}}, corev1.PersistentVolumeFilesystem),
		pv("pv-block", localSource("/mnt/raw"), corev1.PersistentVolumeBlock),
	)
	m, gauge, ctx := newTestManager(t, client, hostRoot)

	// Only the filesystem-mode local PV is scanned.
	owner := Owner{Namespace: "ns", Pod: "db", Volume: "data"}
	m.reconcile(ctx)
	waitValue(t, gauge, 1, "/mnt/disk1", owner, 7)
	if len(m.targets) != 1 {
		t.Fatalf("expected 1 target, got %d", len(m.targets))
	}

	// A failing PV lookup aborts the reconcile: the new hostPath pod is not
	// picked up and the existing target and series are kept.
	other := podWithVolumes("ns", "other", corev1.PodRunning, hostVol("h", "/mnt/other", nil))
	if _, err := client.CoreV1().Pods("ns").Create(ctx, &other, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	client.PrependReactor("get", "persistentvolumes", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("apiserver unavailable")
	})
	m.reconcile(ctx)
	if _, ok := m.targets["/mnt/disk1"]; !ok || len(m.targets) != 1 {
		t.Fatalf("expected only the existing target to be kept, got %v", m.targets)
	}
	waitValue(t, gauge, 1, "/mnt/disk1", owner, 7)

	// Forbidden (missing RBAC) skips local PVs but hostPath discovery goes on.
	client.PrependReactor("get", "persistentvolumes", func(a k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "persistentvolumes"}, a.(k8stesting.GetAction).GetName(), errors.New("rbac"))
	})
	m.reconcile(ctx)
	waitValue(t, gauge, 1, "/mnt/other", Owner{Namespace: "ns", Pod: "other", Volume: "h"}, 3)
	if _, ok := m.targets["/mnt/other"]; !ok || len(m.targets) != 1 {
		t.Fatalf("expected only the hostPath target, got %v", m.targets)
	}
}

func TestManagerIntervalChange(t *testing.T) {
	hostRoot := t.TempDir()
	writeFile(t, hostRoot, "/data", 5)

	pod := annotated(podWithVolumes("ns", "app", corev1.PodRunning, hostVol("data", "/data", nil)),
		map[string]string{AnnotationInterval: "30m"})
	client := fake.NewSimpleClientset(&pod)
	m, gauge, ctx := newTestManager(t, client, hostRoot)
	owner := Owner{Namespace: "ns", Pod: "app", Volume: "data"}

	m.reconcile(ctx)
	waitValue(t, gauge, 1, "/data", owner, 5)
	tg := m.targets["/data"]
	if tg.interval != 30*time.Minute {
		t.Fatalf("expected 30m interval, got %v", tg.interval)
	}

	// Changing the annotation restarts the scanner on the same target; the
	// series stays, and the restart rescans right away.
	pod.Annotations[AnnotationInterval] = "45m"
	if _, err := client.CoreV1().Pods("ns").Update(ctx, &pod, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	writeFile(t, hostRoot, "/data", 9)
	m.reconcile(ctx)
	if m.targets["/data"] != tg || len(m.targets) != 1 {
		t.Fatalf("expected the same single target, got %v", m.targets)
	}
	if tg.interval != 45*time.Minute {
		t.Fatalf("expected 45m interval, got %v", tg.interval)
	}
	waitValue(t, gauge, 1, "/data", owner, 9)

	// Removing the annotation falls back to --auto_interval.
	delete(pod.Annotations, AnnotationInterval)
	if _, err := client.CoreV1().Pods("ns").Update(ctx, &pod, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	m.reconcile(ctx)
	if tg.interval != time.Hour {
		t.Fatalf("expected default 1h interval, got %v", tg.interval)
	}
}
