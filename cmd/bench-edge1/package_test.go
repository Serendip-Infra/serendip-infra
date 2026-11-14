package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestPackageCost(t *testing.T) {
	agg := func(memMean, memMax, cpuMean, cpuMax, nodeMem, nodeMemMax, nodeCPU, nodeCPUMax float64) Aggregate {
		return Aggregate{
			Groups: map[string]GroupAggregate{GroupBuilds: {
				MemoryMiB: Stat{Mean: memMean, Max: memMax}, CPUMillicores: Stat{Mean: cpuMean, Max: cpuMax},
			}},
			NodeMemoryMiB: Stat{Mean: nodeMem, Max: nodeMemMax}, NodeCPUMillicores: Stat{Mean: nodeCPU, Max: nodeCPUMax},
		}
	}
	platform := Aggregate{NodeMemoryMiB: Stat{Mean: 1600}, NodeCPUMillicores: Stat{Mean: 250}}
	tests := []struct {
		name string
		run  PackageRun
		want PackageCost
	}{
		{
			name: "the node's use beyond platform, idle the larger of before and after the builds, rounded up",
			run: PackageRun{
				Provider: "openchoreo-workflow-plane",
				Idle:     agg(40.2, 41, 1.1, 3, 2300.5, 2400, 266, 400),
				Poller:   &PollerRun{Cluster: agg(46.5, 47, 0.9, 2, 2550.2, 2600, 260, 400)},
				Builds: []BuildRun{
					{Cluster: agg(150, 230.4, 900, 1600.2, 2700, 3250.3, 900, 1990)},
					{Cluster: agg(140, 210, 800, 1700, 2800, 3300, 950, 2000)},
				},
			},
			want: PackageCost{
				CostFigures: CostFigures{IdleMemoryMiB: 951, IdleCPUMillicores: 16, PeakMemoryMiB: 1700, PeakCPUMillicores: 1750},
				Own:         CostFigures{IdleMemoryMiB: 47, IdleCPUMillicores: 2, PeakMemoryMiB: 231, PeakCPUMillicores: 1700},
			},
		},
		{
			name: "never below the package's own pods, and peak never below idle, even with no builds",
			run:  PackageRun{Provider: "openchoreo-workflow-plane", Idle: agg(40, 41, 2, 3, 1610, 1620, 240, 300)},
			want: PackageCost{
				CostFigures: CostFigures{IdleMemoryMiB: 40, IdleCPUMillicores: 2, PeakMemoryMiB: 40, PeakCPUMillicores: 2},
				Own:         CostFigures{IdleMemoryMiB: 40, IdleCPUMillicores: 2, PeakMemoryMiB: 40, PeakCPUMillicores: 2},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := packageCost(tc.run, platform); got != tc.want {
				t.Errorf("packageCost = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestPackageCostObservability(t *testing.T) {
	grp := func(memMax, cpuMax, nodeMemMax, nodeCPUMax float64) Aggregate {
		return Aggregate{
			Groups:        map[string]GroupAggregate{GroupObservability: {MemoryMiB: Stat{Mean: memMax - 10, Max: memMax}, CPUMillicores: Stat{Mean: cpuMax / 2, Max: cpuMax}}},
			NodeMemoryMiB: Stat{Mean: nodeMemMax - 50, Max: nodeMemMax}, NodeCPUMillicores: Stat{Mean: nodeCPUMax / 2, Max: nodeCPUMax},
		}
	}
	disk := grp(700, 200, 3000, 900)
	disk.Volumes = []VolumeAggregate{
		{Name: "openchoreo-observability-plane/logs", Group: GroupObservability, FirstUsedMiB: 10, LastUsedMiB: 70, CapacityMiB: 5120, Intervals: 120, SpanSeconds: 7200},
		{Name: "openchoreo-observability-plane/metrics", Group: GroupObservability, FirstUsedMiB: 5, LastUsedMiB: 6, CapacityMiB: 3072, Intervals: 120, SpanSeconds: 7150},
		// Another group's volume, and one seen once, are left out.
		{Name: "openbao/data", Group: GroupSecrets, FirstUsedMiB: 1, LastUsedMiB: 500, CapacityMiB: 1024, Intervals: 120},
		{Name: "openchoreo-observability-plane/late", Group: GroupObservability, FirstUsedMiB: 9, LastUsedMiB: 9, CapacityMiB: 100},
	}
	run := PackageRun{
		Provider: "openchoreo-observability-plane",
		Idle:     grp(600, 40, 2400, 300),
		Load:     &LoadRun{Cluster: grp(650, 400, 2900, 1200), IntervalSeconds: 10},
		Disk:     &LoadRun{Cluster: disk, IntervalSeconds: 60},
	}
	got := packageCost(run, Aggregate{NodeMemoryMiB: Stat{Mean: 1600}, NodeCPUMillicores: Stat{Mean: 250}})
	// Peak is the larger of the load and disk phases; disk is 61 MiB over
	// 2 hours, 732 a day, of 8192 MiB.
	if got.PeakMemoryMiB != 1400 || got.PeakCPUMillicores != 950 || got.Own.PeakMemoryMiB != 700 || got.Own.PeakCPUMillicores != 400 {
		t.Errorf("peak = %+v, own %+v", got.CostFigures, got.Own)
	}
	want := DiskCost{GrowthMiB: 61, SpanSeconds: 7200, GrowthMiBPerDay: 732, CapacityMiB: 8192, Volumes: 2}
	if got.Disk == nil || *got.Disk != want {
		t.Errorf("disk = %+v, want %+v", got.Disk, want)
	}
	if got := diskCost(&LoadRun{Cluster: Aggregate{Volumes: disk.Volumes[2:3]}}, GroupObservability); got != nil {
		t.Errorf("diskCost with none of the group's volumes = %+v, want nil", got)
	}
	run.Cost = got
	sum := renderSummary(Result{Package: &run})
	for _, want := range []string{"Under load (", "Disk phase, still under load (", "**732 MiB a day**"} {
		if !strings.Contains(sum, want) {
			t.Errorf("summary is missing %q", want)
		}
	}
}

func TestParseFlagsRefusesAnUnknownPackage(t *testing.T) {
	if _, err := parseFlags([]string{"--serendipd", "x", "--kubeconfig", "y", "--package", "nope"}); err == nil || !strings.Contains(err.Error(), "openchoreo-observability-plane") {
		t.Errorf("parseFlags = %v, want a refusal naming the known packages", err)
	}
}

func TestParseFlagsPackages(t *testing.T) {
	base := []string{"--serendipd", "x", "--kubeconfig", "y"}
	script := filepath.Join(t.TempDir(), "load.sh")
	if err := os.WriteFile(script, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	obs := []string{"--package", "openchoreo-observability-plane", "--package-load-script", script}
	with := func(extra ...string) []string { return append(slices.Clone(obs), extra...) }
	for _, tc := range []struct {
		name string
		args []string
		ok   bool
	}{
		{"no package", nil, true},
		{"builds", []string{"--package", "openchoreo-workflow-plane"}, true},
		{"observability", obs, true},
		{"observability without the disk phase", with("--package-disk-samples", "0"), true},
		{"no load samples", with("--package-load-samples", "0"), false},
		{"one disk sample", with("--package-disk-samples", "1"), false},
		{"negative disk samples", with("--package-disk-samples", "-1"), false},
		{"zero disk interval", with("--package-disk-interval", "0s"), false},
		{"missing load script", []string{"--package", "openchoreo-observability-plane", "--package-load-script", script + ".missing"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseFlags(append(slices.Clone(base), tc.args...))
			if (err == nil) != tc.ok {
				t.Errorf("parseFlags = %v, want ok %v", err, tc.ok)
			}
		})
	}
}

func TestDiskCostClampsShrinkingStores(t *testing.T) {
	l := &LoadRun{IntervalSeconds: 60, Cluster: Aggregate{Volumes: []VolumeAggregate{
		{Group: GroupObservability, FirstUsedMiB: 50, LastUsedMiB: 40, CapacityMiB: 100, Intervals: 120, SpanSeconds: 7200},
	}}}
	if c := diskCost(l, GroupObservability); c == nil || c.GrowthMiB != 0 || c.GrowthMiBPerDay != 0 {
		t.Errorf("diskCost = %+v, want no growth", c)
	}
}

func TestRenderSummaryMarksAnIncompleteRun(t *testing.T) {
	r := Result{Package: &PackageRun{Provider: "openchoreo-observability-plane", Incomplete: "disk sampling: context canceled"}}
	if got := renderSummary(r); !strings.Contains(got, "Incomplete run, don't state these figures:** disk sampling: context canceled") {
		t.Errorf("summary doesn't mark the run incomplete:\n%s", got)
	}
}

func TestRenderSummaryIncludesThePackage(t *testing.T) {
	if strings.Contains(renderSummary(Result{}), "## Package:") {
		t.Fatal("a run without a package rendered a package section")
	}
	r := Result{Package: &PackageRun{Provider: "openchoreo-workflow-plane", Cost: PackageCost{CostFigures: CostFigures{IdleMemoryMiB: 34, PeakMemoryMiB: 313}}}}
	got := renderSummary(r)
	for _, want := range []string{"## Package: openchoreo-workflow-plane", "### Stated cost (Requirements)", "| 34 MiB |", "| 313 MiB |"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary is missing %q", want)
		}
	}
}
