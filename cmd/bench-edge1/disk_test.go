package main

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

const fixturePVs = `[
 {"spec": {"capacity": {"storage": "5Gi"}, "hostPath": {"path": "/var/local-path-provisioner/pvc-1_obs_data"},
   "claimRef": {"name": "data", "namespace": "obs"},
   "nodeAffinity": {"required": {"nodeSelectorTerms": [{"matchExpressions": [{"key": "kubernetes.io/hostname", "values": ["n1"]}]}]}}},
  "status": {"phase": "Bound"}},
 {"spec": {"capacity": {"storage": "128Mi"}, "local": {"path": "/var/lib/rancher/k3s/storage/pvc-2_ns_db/"},
   "claimRef": {"name": "db", "namespace": "ns"}},
  "status": {"phase": "Bound"}},
 {"spec": {"capacity": {"storage": "1Gi"}, "hostPath": {"path": "/x/other-node"}, "claimRef": {"name": "o", "namespace": "ns"},
   "nodeAffinity": {"required": {"nodeSelectorTerms": [{"matchExpressions": [{"key": "kubernetes.io/hostname", "values": ["n2"]}]}]}}},
  "status": {"phase": "Bound"}},
 {"spec": {"capacity": {"storage": "1Gi"}, "hostPath": {"path": "/x/released"}, "claimRef": {"name": "r", "namespace": "ns"}},
  "status": {"phase": "Released"}},
 {"spec": {"capacity": {"storage": "1Gi"}, "csi": {"driver": "longhorn"}, "claimRef": {"name": "l", "namespace": "ns"}},
  "status": {"phase": "Bound"}}
]`

func TestHostPathVolumes(t *testing.T) {
	var pvs []pvObject
	if err := json.Unmarshal([]byte(fixturePVs), &pvs); err != nil {
		t.Fatal(err)
	}
	got := hostPathVolumes(pvs, "n1")
	want := []hostPathVolume{
		{Claim: "ns/db", Dir: "/var/lib/rancher/k3s/storage/pvc-2_ns_db", CapacityBytes: 128 << 20},
		{Claim: "obs/data", Dir: "/var/local-path-provisioner/pvc-1_obs_data", Node: "n1", CapacityBytes: 5 << 30},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

func TestParseBytesQuantity(t *testing.T) {
	for q, want := range map[string]uint64{"5Gi": 5 << 30, "128Mi": 128 << 20, "1G": 1e9, "1000": 1000, "1.5Ki": 1536, "": 0, "x": 0, "2Xi": 0, "-1Gi": 0, "-5": 0} {
		if got := parseBytesQuantity(q); got != want {
			t.Errorf("%q = %d, want %d", q, got, want)
		}
	}
}

func TestDuProbePodAndParse(t *testing.T) {
	vols := []hostPathVolume{
		{Claim: "a/one", Dir: "/var/lpp/pvc-1_a_one"},
		{Claim: "b/two", Dir: "/var/lpp/pvc-2_b_two"},
		{Claim: "c/three", Dir: "/other/pvc-3"},
	}
	pod, inPod := duProbePod("probe", "n1", vols)
	if pod.Metadata.Namespace != duProbeNamespace || pod.Kind != "Pod" {
		t.Errorf("pod meta = %+v", pod.Metadata)
	}
	spec := string(pod.Spec)
	for _, want := range []string{`"nodeName":"n1"`, `"path":"/var/lpp"`, `"path":"/other"`, `"readOnly":true`, `"/p0/pvc-1_a_one"`, `"/p0/pvc-2_b_two"`, `"/p1/pvc-3"`} {
		if !strings.Contains(spec, want) {
			t.Errorf("spec missing %s: %s", want, spec)
		}
	}
	if strings.Count(spec, `"hostPath"`) != 2 {
		t.Errorf("want one mount per parent directory: %s", spec)
	}
	out := "102400\t/p0/pvc-1_a_one\nbad line\n4\t/p0/pvc-2_b_two\nx\t/p1/pvc-3\n7\t/p9/unknown\n"
	got := parseDu(out, inPod)
	want := map[string]uint64{"a/one": 100 << 20, "b/two": 4 << 10}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseDu = %v, want %v", got, want)
	}
}

func TestAddHostPathUsage(t *testing.T) {
	s := Sample{Volumes: map[string]VolumeUsage{"a/kubelet": {UsedBytes: 1}}}
	addHostPathUsage(&s, map[string]VolumeUsage{"a/kubelet": {UsedBytes: 99}, "b/probe": {UsedBytes: 2}})
	if s.Volumes["a/kubelet"].UsedBytes != 1 || s.Volumes["b/probe"].UsedBytes != 2 {
		t.Errorf("volumes = %v: the kubelet's figure must win, the probe fills the rest", s.Volumes)
	}
	var empty Sample
	addHostPathUsage(&empty, map[string]VolumeUsage{"c/x": {UsedBytes: 3}})
	if empty.Volumes["c/x"].UsedBytes != 3 {
		t.Errorf("nil map not filled: %v", empty.Volumes)
	}
}

func TestParseDuPathWithSpace(t *testing.T) {
	got := parseDu("8\t/p0/dir with space\n", map[string]string{"/p0/dir with space": "a/b"})
	if got["a/b"] != 8<<10 {
		t.Errorf("parseDu = %v", got)
	}
}

func TestVolumeSpanUsesRealTime(t *testing.T) {
	t0 := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	samples := []Sample{
		{At: t0, Volumes: vol(10)},
		{At: t0.Add(10 * time.Second)},
		// The last probe took a while: 70 s after the first, not 2 × 10 s.
		{At: t0.Add(70 * time.Second), Volumes: vol(17)},
	}
	v := aggregateVolumes(samples)[0]
	if v.Intervals != 2 || v.SpanSeconds != 70 {
		t.Fatalf("got %+v", v)
	}
	if got, want := v.GrowthMiBPerDay(10*time.Second), 7*86400.0/70; math.Abs(got-want) > 1e-6 {
		t.Errorf("per day = %v, want %v (from the real span)", got, want)
	}
	// Results from before spec 023 carry no times: the interval count is used.
	old := aggregateVolumes([]Sample{{Volumes: vol(10)}, {Volumes: vol(17)}})[0]
	if old.SpanSeconds != 0 || old.Span(time.Minute) != time.Minute {
		t.Errorf("old = %+v", old)
	}
}

func TestRenderVolumeGrowthNoGap(t *testing.T) {
	out := renderVolumeGrowth([]VolumeGrowth{{Name: "ns/d", UsedA: 1, UsedB: 2, Hours: 0}})
	if !strings.Contains(out, "| ns/d | 1.0 | 2.0 | 0.0 h | — |") {
		t.Errorf("render:\n%s", out)
	}
}
