// Package discovery implements --auto mode: it verifies the exporter runs as a
// DaemonSet pod, finds hostPath volumes and local PersistentVolumes
// (spec.local.path) used by pods on the same node, applies the oap/host-path-*
// pod/PV annotations, and keeps one scanner goroutine running per discovered
// host path.
package discovery

import (
	"context"
	"fmt"
	"os"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const serviceAccountNamespaceFile = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"

// SelfIdentity returns the exporter's own pod name and namespace. The pod name
// comes from POD_NAME (downward API), falling back to the hostname (which is
// the pod name unless hostNetwork is used). The namespace comes from
// POD_NAMESPACE, falling back to the service account namespace file.
func SelfIdentity() (podName, namespace string, err error) {
	podName = os.Getenv("POD_NAME")
	if podName == "" {
		podName, err = os.Hostname()
		if err != nil {
			return "", "", fmt.Errorf("cannot determine own pod name (set POD_NAME via the downward API): %w", err)
		}
	}

	namespace = os.Getenv("POD_NAMESPACE")
	if namespace == "" {
		raw, err := os.ReadFile(serviceAccountNamespaceFile)
		if err != nil {
			return "", "", fmt.Errorf("cannot determine own namespace (set POD_NAMESPACE via the downward API): %w", err)
		}
		namespace = strings.TrimSpace(string(raw))
	}
	return podName, namespace, nil
}

// CheckDaemonSet fetches the exporter's own pod and returns it only if its
// controlling owner is a DaemonSet. Any other owner, no owner at all, or a
// failure to fetch the pod is an error.
func CheckDaemonSet(ctx context.Context, client kubernetes.Interface, podName, namespace string) (*corev1.Pod, error) {
	pod, err := client.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("--auto: cannot get own pod %s/%s: %w", namespace, podName, err)
	}

	owner := metav1.GetControllerOf(pod)
	if owner == nil {
		return nil, fmt.Errorf("--auto requires running as a DaemonSet pod; pod %s/%s has no controller", namespace, podName)
	}
	if owner.Kind != "DaemonSet" {
		return nil, fmt.Errorf("--auto requires running as a DaemonSet pod; pod %s/%s is owned by %s/%s", namespace, podName, owner.Kind, owner.Name)
	}
	if pod.Spec.NodeName == "" {
		return nil, fmt.Errorf("--auto: own pod %s/%s has no spec.nodeName", namespace, podName)
	}
	return pod, nil
}
