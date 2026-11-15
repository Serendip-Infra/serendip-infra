package main

import (
	"math"
	"os"
	"strings"
	"testing"
	"time"
)

const fixtureVolumes = `{
  "node": {"nodeName": "n", "memory": {"workingSetBytes": 1}},
  "pods": [
    {"podRef": {"name": "openobserve-0", "namespace": "openchoreo-observability-plane"},
     "memory": {"workingSetBytes": 1048576},
     "volume": [
       {"name": "data", "pvcRef": {"name": "data-openobserve-0", "namespace": "openchoreo-observability-plane"},
        "usedBytes": 104857600, "capacityBytes": 5368709120},
       {"name": "kube-api-access", "usedBytes": 12288, "capacityBytes": 1000}
     ]},
    {"podRef": {"name": "reader", "namespace": "openchoreo-observability-plane"},
     "memory": {"workingSetBytes": 1048576},
     "volume": [
       {"name": "data", "pvcRef": {"name": "data-openobserve-0", "namespace": "openchoreo-observability-plane"},
        "usedBytes": 104857600, "capacityBytes": 5368709120},
       {"name": "unknown-use", "pvcRef": {"name": "pending", "namespace": "x"}}
     ]}
  ]
}`

func TestSampleVolumes(t *testing.T) {
	s, err := parseSummary([]byte(fixtureVolumes))
	if err != nil {
		t.Fatal(err)
	}
	smp := sampleFrom(s)
	if len(smp.Volumes) != 1 {
		t.Fatalf("volumes = %v, want only the PVC with stats, counted once", smp.Volumes)
	}
	v := smp.Volumes["openchoreo-observability-plane/data-openobserve-0"]
	if v.UsedBytes != 104857600 || v.CapacityBytes != 5368709120 {
		t.Errorf("volume = %+v", v)
	}
	if _, ok := smp.Groups[GroupObservability]; !ok {
		t.Errorf("observability namespace not grouped; groups = %v", smp.Groups)
	}
}

func vol(used uint64) map[string]VolumeUsage {
	return map[string]VolumeUsage{"ns/data": {UsedBytes: used << 20, CapacityBytes: 1 << 30}}
}

func TestAggregateVolumesAndGrowth(t *testing.T) {
	tests := []struct {
		name      string
		samples   []Sample
		interval  time.Duration
		wantFirst float64
		wantLast  float64
		wantInt   int
		wantDay   float64
	}{
		{"one sample has no growth", []Sample{{Volumes: vol(10)}}, time.Minute, 10, 10, 0, 0},
		{"growth over two hours", []Sample{{Volumes: vol(10)}, {Volumes: vol(15)}, {Volumes: vol(20)}}, time.Hour, 10, 20, 2, 120},
		{"appears late", []Sample{{}, {}, {Volumes: vol(4)}, {Volumes: vol(6)}}, 30 * time.Minute, 4, 6, 1, 96},
		{"shrinks", []Sample{{Volumes: vol(20)}, {Volumes: vol(8)}}, 12 * time.Hour, 20, 8, 1, -24},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vs := aggregateVolumes(tt.samples)
			if len(vs) != 1 {
				t.Fatalf("volumes = %v", vs)
			}
			v := vs[0]
			if v.FirstUsedMiB != tt.wantFirst || v.LastUsedMiB != tt.wantLast || v.Intervals != tt.wantInt || v.CapacityMiB != 1024 {
				t.Errorf("got %+v", v)
			}
			if got := v.GrowthMiBPerDay(tt.interval); math.Abs(got-tt.wantDay) > 1e-9 {
				t.Errorf("per day = %v, want %v", got, tt.wantDay)
			}
		})
	}
	if aggregateVolumes([]Sample{{}, {}}) != nil {
		t.Error("no volumes should aggregate to nil")
	}
}

func TestWriteVolumeTable(t *testing.T) {
	var b strings.Builder
	writeVolumeTable(&b, []VolumeAggregate{{Name: "ns/data", Group: "Other: ns", FirstUsedMiB: 10, LastUsedMiB: 20, CapacityMiB: 1024, Intervals: 2}}, time.Hour)
	out := b.String()
	for _, want := range []string{"| ns/data | Other: ns | 10.0 | 20.0 | 1024 | +10.0 MiB in 2h0m0s | 120.0 MiB |"} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q:\n%s", want, out)
		}
	}
	b.Reset()
	writeVolumeTable(&b, nil, time.Hour)
	if b.Len() != 0 {
		t.Errorf("empty volumes rendered %q", b.String())
	}
}

func TestCompareVolumes(t *testing.T) {
	start := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	a := Result{StartedAt: start, Platform: Aggregate{Volumes: []VolumeAggregate{{Name: "ns/data", LastUsedMiB: 100}, {Name: "ns/gone", LastUsedMiB: 5}}}}
	b := Result{
		StartedAt: start.Add(12 * time.Hour),
		Platform:  Aggregate{Volumes: []VolumeAggregate{{Name: "ns/data", LastUsedMiB: 1}}},
		// The package's idle phase is the last one measured, so it wins.
		Package: &PackageRun{IdleSamples: []Sample{{}}, Idle: Aggregate{Volumes: []VolumeAggregate{{Name: "ns/data", LastUsedMiB: 150}, {Name: "ns/new", LastUsedMiB: 7}}}},
	}
	gs := compareVolumes(a, b)
	if len(gs) != 2 {
		t.Fatalf("growth = %+v", gs)
	}
	if g := gs[0]; g.Name != "ns/data" || g.UsedA != 100 || g.UsedB != 150 || g.Hours != 12 || g.PerDay != 100 || g.OnlyInB {
		t.Errorf("data = %+v", g)
	}
	if g := gs[1]; g.Name != "ns/new" || !g.OnlyInB || g.PerDay != 0 {
		t.Errorf("new = %+v", g)
	}
	out := renderVolumeGrowth(gs)
	if !strings.Contains(out, "| ns/data | 100.0 | 150.0 | 12.0 h | +100.0 MiB |") || !strings.Contains(out, "| ns/new | — | 7.0 | 12.0 h | new |") {
		t.Errorf("render:\n%s", out)
	}
	if renderVolumeGrowth(nil) != "" {
		t.Error("no volumes should render nothing")
	}
}

// TestLiveVolumes runs the real measurement against a cluster: the
// kubelet's volume stats, then the host-path probe pod, and prints the
// volume table. It skips unless BENCH_KUBECONFIG names a kubeconfig.
func TestLiveVolumes(t *testing.T) {
	kc := os.Getenv("BENCH_KUBECONFIG")
	if kc == "" {
		t.Skip("BENCH_KUBECONFIG not set")
	}
	cl, err := newCluster(kc)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	n, err := cl.node(ctx)
	if err != nil {
		t.Fatal(err)
	}
	s, err := cl.summary(ctx, n.Name)
	if err != nil {
		t.Fatal(err)
	}
	smp := sampleFrom(s)
	u, err := cl.hostPathUsage(ctx, n.Name)
	if err != nil {
		t.Fatal(err)
	}
	addHostPathUsage(&smp, u)
	if len(smp.Volumes) == 0 {
		t.Fatal("no volumes measured")
	}
	var b strings.Builder
	writeVolumeTable(&b, aggregate([]Sample{smp}).Volumes, 0)
	t.Log("\n" + b.String())
}
