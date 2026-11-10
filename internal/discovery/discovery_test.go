package discovery

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

	"hostpath-size-exporter/internal/metrics"
)

func boolPtr(b bool) *bool { return &b }

func hostPathType(t corev1.HostPathType) *corev1.HostPathType { return &t }

func ownedPod(name, kind string) *corev1.Pod {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "monitoring", UID: types.UID(name)},
		Spec:       corev1.PodSpec{NodeName: "node-1"},
	}
	if kind != "" {
		pod.OwnerReferences = []metav1.OwnerReference{{Kind: kind, Name: "owner", Controller: boolPtr(true)}}
	}
	return pod
}

func TestCheckDaemonSet(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset(
		ownedPod("ds-pod", "DaemonSet"),
		ownedPod("rs-pod", "ReplicaSet"),
		ownedPod("bare-pod", ""),
	)

	pod, err := CheckDaemonSet(ctx, client, "ds-pod", "monitoring")
	if err != nil {
		t.Fatalf("DaemonSet pod: unexpected error: %v", err)
	}
	if pod.Spec.NodeName != "node-1" {
		t.Fatalf("expected node-1, got %q", pod.Spec.NodeName)
	}

	for _, tc := range []struct{ pod, want string }{
		{"rs-pod", "ReplicaSet"},
		{"bare-pod", "no controller"},
		{"missing-pod", "cannot get own pod"},
	} {
		_, err := CheckDaemonSet(ctx, client, tc.pod, "monitoring")
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: expected error containing %q, got %v", tc.pod, tc.want, err)
		}
	}
}

func podWithVolumes(ns, name string, phase corev1.PodPhase, vols ...corev1.Volume) corev1.Pod {
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, UID: types.UID(ns + "/" + name)},
		Spec:       corev1.PodSpec{NodeName: "node-1", Volumes: vols},
		Status:     corev1.PodStatus{Phase: phase},
	}
}

func hostVol(name, path string, typ *corev1.HostPathType) corev1.Volume {
	return corev1.Volume{Name: name, VolumeSource: corev1.VolumeSource{
		HostPath: &corev1.HostPathVolumeSource{Path: path, Type: typ},
	}}
}

func TestDiscover(t *testing.T) {
	pods := []corev1.Pod{
		podWithVolumes("monitoring", "exporter", corev1.PodRunning, hostVol("host", "/", nil)),
		podWithVolumes("b", "app", corev1.PodRunning,
			hostVol("data", "/var/lib/app/", hostPathType(corev1.HostPathDirectory)),
			hostVol("sock", "/run/containerd.sock", hostPathType(corev1.HostPathSocket)),
			hostVol("conf", "/etc/app.conf", hostPathType(corev1.HostPathFile)),
			corev1.Volume{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		),
		podWithVolumes("a", "app2", corev1.PodPending, hostVol("shared", "/var/lib/app", nil)),
		podWithVolumes("a", "done", corev1.PodSucceeded, hostVol("old", "/var/lib/old", nil)),
		podWithVolumes("c", "logs", corev1.PodRunning,
			hostVol("logs", "/var/log", nil),
			hostVol("nested", "/var/log/pods", nil),
			hostVol("root", "/", nil),
		),
	}

	got := Discover(pods, types.UID("monitoring/exporter"), []string{"/var/log", "/"}, nil, time.Minute, nil)
	want := map[string]Desired{
		"/var/lib/app": {Interval: time.Minute, Owners: []Owner{
			{Namespace: "a", Pod: "app2", Volume: "shared"},
			{Namespace: "b", Pod: "app", Volume: "data"},
		}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Discover = %+v, want %+v", got, want)
	}
}

func TestExcluded(t *testing.T) {
	for _, tc := range []struct {
		path     string
		excludes []string
		want     bool
	}{
		{"/var/log", []string{"/var/log"}, true},
		{"/var/log/pods", []string{"/var/log/"}, true},
		{"/var/logs", []string{"/var/log"}, false},
		{"/", []string{"/"}, true},
		{"/data", []string{"/"}, false},
		{"/data", nil, false},
	} {
		if got := Excluded(tc.path, tc.excludes); got != tc.want {
			t.Errorf("Excluded(%q, %v) = %v, want %v", tc.path, tc.excludes, got, tc.want)
		}
	}
}

func TestManagerReconcile(t *testing.T) {
	hostRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(hostRoot, "data", "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hostRoot, "data", "app", "f.bin"), make([]byte, 42), 0o644); err != nil {
		t.Fatal(err)
	}

	pod1 := podWithVolumes("ns", "app-1", corev1.PodRunning, hostVol("data", "/data/app", nil))
	pod2 := podWithVolumes("ns", "app-2", corev1.PodRunning, hostVol("data", "/data/app", nil))
	other := podWithVolumes("ns", "other-node", corev1.PodRunning, hostVol("data", "/data/app", nil))
	other.Spec.NodeName = "node-2"
	client := fake.NewSimpleClientset(&pod1, &other)

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
	defer func() {
		cancel()
		for _, tg := range m.targets {
			tg.cancel()
		}
		m.wg.Wait()
	}()

	value := func(o Owner) float64 {
		return testutil.ToFloat64(gauge.WithLabelValues(o.Name(), "/data/app", o.Namespace, o.Pod, o.Volume))
	}
	waitSeries := func(n int) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for testutil.CollectAndCount(gauge) != n {
			if time.Now().After(deadline) {
				t.Fatalf("expected %d series, got %d", n, testutil.CollectAndCount(gauge))
			}
			time.Sleep(5 * time.Millisecond)
		}
	}

	// New pod on this node: one series with the scanned size; the other
	// node's pod is filtered out by the field selector.
	m.reconcile(ctx)
	waitSeries(1)
	o1 := Owner{Namespace: "ns", Pod: "app-1", Volume: "data"}
	if v := value(o1); v != 42 {
		t.Fatalf("expected 42 bytes, got %v", v)
	}

	// A second pod sharing the path gets its series immediately, from the
	// last scan, without a second scanner goroutine.
	if _, err := client.CoreV1().Pods("ns").Create(ctx, &pod2, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	m.reconcile(ctx)
	waitSeries(2)
	if len(m.targets) != 1 {
		t.Fatalf("expected 1 target, got %d", len(m.targets))
	}
	if v := value(Owner{Namespace: "ns", Pod: "app-2", Volume: "data"}); v != 42 {
		t.Fatalf("expected 42 bytes for second owner, got %v", v)
	}

	// Removing one owner deletes only its series.
	if err := client.CoreV1().Pods("ns").Delete(ctx, "app-1", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	m.reconcile(ctx)
	waitSeries(1)

	// Removing the last owner stops the target and deletes the path's series.
	if err := client.CoreV1().Pods("ns").Delete(ctx, "app-2", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	m.reconcile(ctx)
	waitSeries(0)
	if len(m.targets) != 0 {
		t.Fatalf("expected no targets, got %d", len(m.targets))
	}
}
