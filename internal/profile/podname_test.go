package profile_test

import (
	"slices"
	"testing"

	"github.com/meiserloh/sheeshlog/internal/profile"
)

func TestPodCandidatesShapes(t *testing.T) {
	tests := []struct {
		what     string
		pod      string
		want     []string
		workload string
	}{
		{
			what:     "Deployment: ReplicaSet hash plus pod suffix",
			pod:      "my-backup-operator-controller-manager-7c45d48bb4-cgkp2",
			want:     []string{"my-backup-operator-controller-manager-7c45d48bb4-cgkp2", "my-backup-operator-controller-manager-7c45d48bb4", "my-backup-operator-controller-manager"},
			workload: "my-backup-operator-controller-manager",
		},
		{
			what:     "DaemonSet: one generated suffix",
			pod:      "k8s-loki-canary-wq7zz",
			want:     []string{"k8s-loki-canary-wq7zz", "k8s-loki-canary", "k8s-loki"},
			workload: "k8s-loki-canary",
		},
		{
			what:     "StatefulSet: one ordinal suffix",
			pod:      "k8s-loki-0",
			want:     []string{"k8s-loki-0", "k8s-loki", "k8s"},
			workload: "k8s-loki",
		},
		{
			what:     "StatefulSet: a repeating name",
			pod:      "prometheus-k8s-prometheus-prometheus-0",
			want:     []string{"prometheus-k8s-prometheus-prometheus-0", "prometheus-k8s-prometheus-prometheus", "prometheus-k8s-prometheus"},
			workload: "prometheus-k8s-prometheus-prometheus",
		},
		{
			what:     "a directly named pod",
			pod:      "velero",
			want:     []string{"velero"},
			workload: "velero",
		},
	}

	for _, tt := range tests {
		t.Run(tt.what, func(t *testing.T) {
			got := profile.PodCandidates(tt.pod)
			if !slices.Equal(got, tt.want) {
				t.Errorf("PodCandidates(%q) = %q, want %q", tt.pod, got, tt.want)
			}
			if !slices.Contains(got, tt.workload) {
				t.Errorf("PodCandidates(%q) never offers the workload %q", tt.pod, tt.workload)
			}
		})
	}
}

// The candidates run most specific first, because that is what decides which
// installed profile wins when more than one exists.
func TestPodCandidatesRunMostSpecificFirst(t *testing.T) {
	got := profile.PodCandidates("a-b-c")
	want := []string{"a-b-c", "a-b", "a"}
	if !slices.Equal(got, want) {
		t.Errorf("PodCandidates(\"a-b-c\") = %q, want %q", got, want)
	}
}

// Trimming has to stop at a name, never reach the empty string or a bare
// leading hyphen: neither names a profile anyone could install.
func TestPodCandidatesNeverProposeAnUninstallableName(t *testing.T) {
	for _, pod := range []string{"", "-", "-loki", "loki-", "--"} {
		for _, name := range profile.PodCandidates(pod) {
			if err := profile.ValidName(name); err != nil {
				t.Errorf("PodCandidates(%q) offers %q, which is not a profile name: %v", pod, name, err)
			}
		}
	}
}
