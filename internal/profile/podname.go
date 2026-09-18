package profile

import (
	"errors"
	"fmt"
	"strings"
)

// podSuffixes is how many generated hyphen-delimited suffixes a pod name may
// carry over its workload's name. Two covers the deepest observed shape — a
// Deployment's ReplicaSet hash plus the pod's own suffix — and DaemonSet and
// StatefulSet pods carry one, so trying up to two also covers them.
const podSuffixes = 2

// PodCandidates returns the profile names to try for a pod, most specific
// first: the name as given, then the name with its last hyphen-delimited
// suffix removed, then with its last two removed.
func PodCandidates(pod string) []string {
	if pod == "" {
		return nil
	}
	candidates := []string{pod}
	for range podSuffixes {
		last := candidates[len(candidates)-1]

		cut := strings.LastIndex(last, "-")
		if cut <= 0 {
			break
		}
		candidates = append(candidates, last[:cut])
	}
	return candidates
}

// A NoProfileForPodError reports a pod name that no installed profile is named
// after
type NoProfileForPodError struct {
	Pod string
	// Tried is the candidates actually looked for
	Tried []string
}

func (e *NoProfileForPodError) Error() string {
	if len(e.Tried) == 0 {
		return fmt.Sprintf("no profile for pod %q (no candidate name is a profile name)", e.Pod)
	}
	return fmt.Sprintf("no profile for pod %q (tried %s)", e.Pod, strings.Join(e.Tried, ", "))
}

// ForPod loads the profile for a pod: the first of the pod's candidates that is
// installed, most specific first. With none installed it returns a
// NoProfileForPodError
func ForPod(configRoot, pod string) (Profile, error) {
	var tried []string
	for _, name := range PodCandidates(pod) {
		if ValidName(name) != nil {
			continue
		}
		tried = append(tried, name)

		p, err := Load(configRoot, name)
		if err == nil {
			return p, nil
		}

		var notFound *NotFoundError
		if !errors.As(err, &notFound) {
			return Profile{}, err
		}
	}
	return Profile{}, &NoProfileForPodError{Pod: pod, Tried: tried}
}
