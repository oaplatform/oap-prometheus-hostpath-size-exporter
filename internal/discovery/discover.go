package discovery

import (
	"path"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

// Owner identifies one pod volume that declares a hostPath.
type Owner struct {
	Namespace string
	Pod       string
	Volume    string
}

// Name is the value used for the metric's name label: namespace/pod/volume.
func (o Owner) Name() string {
	return o.Namespace + "/" + o.Pod + "/" + o.Volume
}

// nonDirectoryTypes are hostPath types that can never be a directory, so
// there is nothing to walk.
var nonDirectoryTypes = map[corev1.HostPathType]bool{
	corev1.HostPathFile:         true,
	corev1.HostPathFileOrCreate: true,
	corev1.HostPathSocket:       true,
	corev1.HostPathCharDev:      true,
	corev1.HostPathBlockDev:     true,
}

// Discover returns every hostPath declared by pods, keyed by cleaned host
// path, with the pod volumes that declare it (sorted). The exporter's own pod
// (self), finished pods, non-directory hostPath types and excluded paths are
// skipped.
func Discover(pods []corev1.Pod, self types.UID, excludes []string) map[string][]Owner {
	result := make(map[string][]Owner)
	for i := range pods {
		pod := &pods[i]
		if pod.UID == self {
			continue
		}
		if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			continue
		}
		for _, vol := range pod.Spec.Volumes {
			hp := vol.HostPath
			if hp == nil || hp.Path == "" {
				continue
			}
			if hp.Type != nil && nonDirectoryTypes[*hp.Type] {
				continue
			}
			p := path.Clean(hp.Path)
			if !strings.HasPrefix(p, "/") || Excluded(p, excludes) {
				continue
			}
			result[p] = append(result[p], Owner{Namespace: pod.Namespace, Pod: pod.Name, Volume: vol.Name})
		}
	}

	for _, owners := range result {
		sort.Slice(owners, func(i, j int) bool {
			a, b := owners[i], owners[j]
			if a.Namespace != b.Namespace {
				return a.Namespace < b.Namespace
			}
			if a.Pod != b.Pod {
				return a.Pod < b.Pod
			}
			return a.Volume < b.Volume
		})
	}
	return result
}

// Excluded reports whether p equals an exclude or lies under one. An exclude
// of "/" matches only the root path itself, not everything under it.
func Excluded(p string, excludes []string) bool {
	for _, ex := range excludes {
		ex = path.Clean(ex)
		if p == ex {
			return true
		}
		if ex != "/" && strings.HasPrefix(p, ex+"/") {
			return true
		}
	}
	return false
}
