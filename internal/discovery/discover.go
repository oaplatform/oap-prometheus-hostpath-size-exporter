package discovery

import (
	"path"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

// Owner identifies one pod volume that uses a host path (a hostPath volume or
// a claim bound to a local PersistentVolume).
type Owner struct {
	Namespace string
	Pod       string
	Volume    string
}

// Name is the value used for the metric's name label: namespace/pod/volume.
func (o Owner) Name() string {
	return o.Namespace + "/" + o.Pod + "/" + o.Volume
}

// ClaimKey identifies a PersistentVolumeClaim.
type ClaimKey struct {
	Namespace string
	Name      string
}

// LocalPV is a local PersistentVolume a claim is bound to.
type LocalPV struct {
	Name        string
	Path        string
	Annotations map[string]string
}

// Desired is one host path to scan: the pod volumes using it and its scan
// interval.
type Desired struct {
	Owners   []Owner
	Interval time.Duration
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

// skipPod reports whether pod is never considered: the exporter's own pod or a
// finished pod.
func skipPod(pod *corev1.Pod, self types.UID) bool {
	return pod.UID == self || pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed
}

// claimName returns the PVC a volume uses: a persistentVolumeClaim volume's
// claim, or the claim generated for a generic ephemeral volume.
func claimName(pod *corev1.Pod, vol *corev1.Volume) (string, bool) {
	if pvc := vol.PersistentVolumeClaim; pvc != nil && pvc.ClaimName != "" {
		return pvc.ClaimName, true
	}
	if vol.Ephemeral != nil {
		return pod.Name + "-" + vol.Name, true
	}
	return "", false
}

// Claims returns the distinct PVCs used by pods (sorted), skipping the same
// pods Discover skips.
func Claims(pods []corev1.Pod, self types.UID) []ClaimKey {
	seen := make(map[ClaimKey]bool)
	var result []ClaimKey
	for i := range pods {
		pod := &pods[i]
		if skipPod(pod, self) {
			continue
		}
		for j := range pod.Spec.Volumes {
			name, ok := claimName(pod, &pod.Spec.Volumes[j])
			if !ok {
				continue
			}
			k := ClaimKey{Namespace: pod.Namespace, Name: name}
			if !seen[k] {
				seen[k] = true
				result = append(result, k)
			}
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Namespace != result[j].Namespace {
			return result[i].Namespace < result[j].Namespace
		}
		return result[i].Name < result[j].Name
	})
	return result
}

// Discover returns every host path used by pods, keyed by cleaned host path:
// hostPath volumes, and claims that localPVs resolves to a local PV path. The
// exporter's own pod (self), finished pods, non-directory hostPath types,
// excluded paths and pod volumes excluded by annotation are skipped. Each
// path's owners are sorted; its interval is the smallest effective interval of
// its owners (annotation, else defInterval). Unparsable annotations are
// reported through warn, which may be nil.
func Discover(pods []corev1.Pod, self types.UID, excludes []string, localPVs map[ClaimKey]LocalPV, defInterval time.Duration, warn WarnFunc) map[string]Desired {
	if warn == nil {
		warn = func(string, string, string, error) {}
	}
	pvSettings := make(map[string]settings)
	result := make(map[string]Desired)
	for i := range pods {
		pod := &pods[i]
		if skipPod(pod, self) {
			continue
		}
		podObj := "pod " + pod.Namespace + "/" + pod.Name
		podSet := parseSettings(pod.Annotations, func(key, value string, err error) { warn(podObj, key, value, err) })

		for j := range pod.Spec.Volumes {
			vol := &pod.Spec.Volumes[j]
			var raw string
			set := podSet
			if hp := vol.HostPath; hp != nil {
				if hp.Type != nil && nonDirectoryTypes[*hp.Type] {
					continue
				}
				raw = hp.Path
			} else {
				claim, ok := claimName(pod, vol)
				if !ok {
					continue
				}
				pv, ok := localPVs[ClaimKey{Namespace: pod.Namespace, Name: claim}]
				if !ok {
					continue
				}
				s, ok := pvSettings[pv.Name]
				if !ok {
					pvObj := "persistentvolume " + pv.Name
					s = parseSettings(pv.Annotations, func(key, value string, err error) { warn(pvObj, key, value, err) })
					pvSettings[pv.Name] = s
				}
				set = merge(s, podSet)
				raw = pv.Path
			}
			if raw == "" || set.exclude {
				continue
			}
			p := path.Clean(raw)
			if !strings.HasPrefix(p, "/") || Excluded(p, excludes) {
				continue
			}

			interval := set.interval
			if interval <= 0 {
				interval = defInterval
			}
			d := result[p]
			d.Owners = append(d.Owners, Owner{Namespace: pod.Namespace, Pod: pod.Name, Volume: vol.Name})
			if d.Interval <= 0 || interval < d.Interval {
				d.Interval = interval
			}
			result[p] = d
		}
	}

	for _, d := range result {
		owners := d.Owners
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
