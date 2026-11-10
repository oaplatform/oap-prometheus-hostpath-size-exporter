package discovery

import (
	"fmt"
	"strconv"
	"time"
)

// Annotations that tune auto mode per pod or per PersistentVolume. On a local
// PV volume a PV annotation takes precedence over the same pod annotation.
const (
	// AnnotationInterval is the scan interval (Go duration) for the paths of
	// the annotated pod or PV, overriding --auto_interval.
	AnnotationInterval = "oap/host-path-interval"
	// AnnotationRefresh is an alias of AnnotationInterval; AnnotationInterval
	// wins when both are set.
	AnnotationRefresh = "oap/host-path-refresh"
	// AnnotationExclude ("true"/"false") excludes the annotated pod's volumes,
	// or every user of the annotated PV, from scanning.
	AnnotationExclude = "oap/host-path-exclude"
)

// WarnFunc reports an annotation that could not be parsed. object identifies
// where it was found, e.g. "pod ns/name" or "persistentvolume name".
type WarnFunc func(object, key, value string, err error)

// settings are the parsed annotations of one object. Zero values mean unset.
type settings struct {
	interval   time.Duration
	exclude    bool
	excludeSet bool
}

// parseSettings parses the auto mode annotations of one object. Invalid values
// are reported through warn and ignored.
func parseSettings(ann map[string]string, warn func(key, value string, err error)) settings {
	var s settings
	for _, key := range []string{AnnotationInterval, AnnotationRefresh} {
		v, ok := ann[key]
		if !ok {
			continue
		}
		d, err := time.ParseDuration(v)
		if err == nil && d <= 0 {
			err = fmt.Errorf("must be positive")
		}
		if err != nil {
			warn(key, v, err)
			continue
		}
		s.interval = d
		break
	}
	if v, ok := ann[AnnotationExclude]; ok {
		b, err := strconv.ParseBool(v)
		if err != nil {
			warn(AnnotationExclude, v, err)
		} else {
			s.exclude, s.excludeSet = b, true
		}
	}
	return s
}

// merge returns primary with every unset value taken from fallback.
func merge(primary, fallback settings) settings {
	if primary.interval <= 0 {
		primary.interval = fallback.interval
	}
	if !primary.excludeSet {
		primary.exclude, primary.excludeSet = fallback.exclude, fallback.excludeSet
	}
	return primary
}
