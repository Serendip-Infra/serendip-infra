package main

import (
	"strings"
	"testing"
	"time"
)

func TestDeltaOf(t *testing.T) {
	base := Aggregate{
		NodeMemoryMiB: Stat{Mean: 2000, Max: 2100}, NodeCPUMillicores: Stat{Mean: 300, Max: 400},
		Groups: map[string]GroupAggregate{GroupKubernetes: {MemoryMiB: Stat{Mean: 900}}, "Other: gone": {MemoryMiB: Stat{Mean: 5}}},
		Pods:   []PodAggregate{{Name: "kube-system/kube-apiserver", MemoryMiB: Stat{Mean: 900}}},
	}
	phase := Aggregate{
		NodeMemoryMiB: Stat{Mean: 2600, Max: 2900}, NodeCPUMillicores: Stat{Mean: 350, Max: 1300},
		Groups: map[string]GroupAggregate{GroupKubernetes: {MemoryMiB: Stat{Mean: 1070}}, GroupObservability: {MemoryMiB: Stat{Mean: 300}}},
		Pods: []PodAggregate{
			{Name: "kube-system/kube-apiserver", MemoryMiB: Stat{Mean: 1070}},
			{Name: "obs/openobserve-0", MemoryMiB: Stat{Mean: 120}},
		},
	}
	d := deltaOf(base, phase)
	if d.NodeMemoryMiB != 600 || d.NodeMemoryMaxMiB != 900 || d.NodeCPUMilli != 50 || d.NodeCPUMaxMilli != 1000 {
		t.Errorf("node delta = %+v", d)
	}
	if d.Groups[GroupKubernetes] != 170 || d.Groups[GroupObservability] != 300 || d.Groups["Other: gone"] != -5 {
		t.Errorf("groups = %v", d.Groups)
	}
	if len(d.Pods) != 2 || d.Pods[0].Name != "kube-system/kube-apiserver" || d.Pods[0].DeltaMB != 170 || d.Pods[1].DeltaMB != 120 {
		t.Errorf("pods = %+v", d.Pods)
	}
	out := renderDelta("baseline", "B idle", d)
	for _, want := range []string{"### B idle over baseline", "+600 MiB mean, +900 MiB at its max", "| Kubernetes system | +170.0 |", "| Observability | +300.0 |", "| kube-system/kube-apiserver | +170.0 |"} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q:\n%s", want, out)
		}
	}
}

func TestRenderSampleRun(t *testing.T) {
	r := SampleRun{
		Label: "B: OpenObserve, idle", StartedAt: time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC), IntervalSeconds: 10,
		Simulation: Simulation{CPUs: 2, CPUSet: "0-3", Note: "kind at 2 CPUs", NotSimulated: []string{"RAM"}},
		Samples:    []Sample{{}, {}},
		Cluster:    Aggregate{Groups: map[string]GroupAggregate{GroupObservability: {}}},
	}
	out := renderSampleRun(r)
	for _, want := range []string{"# B: OpenObserve, idle", "SIMULATION", "Not simulated: RAM", "2 samples every 10s", "| Observability |"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
}
