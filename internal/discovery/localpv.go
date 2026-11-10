package discovery

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// resolveLocalPVs maps each claim bound to a local, filesystem-mode PV to that
// PV. Missing objects and unbound or non-local claims are skipped. A Forbidden
// error (no RBAC for PVCs/PVs) is logged and the claim skipped, so hostPath
// discovery keeps working; any other API error is returned.
func (m *Manager) resolveLocalPVs(ctx context.Context, claims []ClaimKey) (map[ClaimKey]LocalPV, error) {
	result := make(map[ClaimKey]LocalPV)
	var forbidden error
	for _, c := range claims {
		pvc, err := m.Client.CoreV1().PersistentVolumeClaims(c.Namespace).Get(ctx, c.Name, metav1.GetOptions{})
		if err != nil {
			if err := m.classify(err, &forbidden, "persistentvolumeclaim", c.Namespace+"/"+c.Name); err != nil {
				return nil, err
			}
			continue
		}
		if pvc.Spec.VolumeName == "" {
			continue
		}

		pv, err := m.Client.CoreV1().PersistentVolumes().Get(ctx, pvc.Spec.VolumeName, metav1.GetOptions{})
		if err != nil {
			if err := m.classify(err, &forbidden, "persistentvolume", pvc.Spec.VolumeName); err != nil {
				return nil, err
			}
			continue
		}
		if pv.Spec.Local == nil || pv.Spec.Local.Path == "" {
			continue
		}
		if pv.Spec.VolumeMode != nil && *pv.Spec.VolumeMode == corev1.PersistentVolumeBlock {
			continue
		}
		result[c] = LocalPV{Name: pv.Name, Path: pv.Spec.Local.Path, Annotations: pv.Annotations}
	}

	if forbidden != nil {
		m.Logger.Error("not allowed to get persistentvolumeclaims/persistentvolumes; local PVs are not discovered (check RBAC)", "error", forbidden)
	}
	return result, nil
}

// classify handles an API error from resolving a claim: NotFound and
// Forbidden mean "skip this claim" and return nil (Forbidden is recorded in
// forbidden); anything else is returned wrapped.
func (m *Manager) classify(err error, forbidden *error, kind, name string) error {
	switch {
	case apierrors.IsNotFound(err):
		m.Logger.Debug(kind+" not found, skipping", "name", name)
		return nil
	case apierrors.IsForbidden(err):
		*forbidden = err
		return nil
	default:
		return fmt.Errorf("getting %s %s: %w", kind, name, err)
	}
}
