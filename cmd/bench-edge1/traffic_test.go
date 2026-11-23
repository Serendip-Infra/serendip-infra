package main

import (
	"strings"
	"testing"
	"time"
)

func TestGroupForPod(t *testing.T) {
	tests := []struct{ ns, name, want string }{
		{"kube-system", "cilium-abcde", GroupNetworking},
		{"kube-system", "cilium-operator-6d9f-xyz", GroupNetworking},
		{"kube-system", "cilium-envoy-q2w3e", GroupNetworking},
		{"kube-system", "hubble-relay-1", GroupNetworking},
		{"kube-system", "coredns-123", GroupKubernetes},
		{"openchoreo-observability-plane", "opentelemetry-collector-7c9-abc", GroupTracing},
		{"openchoreo-observability-plane", "prometheus-0", GroupObservability},
		{"default", "cilium-x", "Other: default"},
	}
	for _, tt := range tests {
		if got := groupForPod(tt.ns, tt.name); got != tt.want {
			t.Errorf("groupForPod(%q, %q) = %q, want %q", tt.ns, tt.name, got, tt.want)
		}
	}
}

func TestCheckLoadFlags_Traffic(t *testing.T) {
	base := packageOptions{loadScript: "traffic.go", loadSamples: 1, trafficImage: "img", trafficCallMs: 100, trafficSamples: 60}
	ok := base
	ok.traffic, ok.tracing = true, true
	if err := checkLoadFlags(ok); err != nil {
		t.Errorf("traffic and tracing: %v", err)
	}
	noTraffic := base
	noTraffic.tracing = true
	if err := checkLoadFlags(noTraffic); err == nil || !strings.Contains(err.Error(), "--package-tracing needs --package-traffic") {
		t.Errorf("tracing without traffic: %v", err)
	}
	few := ok
	few.trafficSamples = 1
	if err := checkLoadFlags(few); err == nil {
		t.Error("one traffic sample: accepted")
	}
}

func TestWriteTrafficSummary(t *testing.T) {
	phase := func(nodeMiB, obsMiB float64) *LoadRun {
		l := &LoadRun{IntervalSeconds: 10}
		l.Cluster.NodeMemoryMiB = Stat{Mean: nodeMiB, Max: nodeMiB + 10}
		l.Cluster.NodeCPUMillicores = Stat{Mean: 300, Max: 400}
		l.Cluster.Groups = map[string]GroupAggregate{
			GroupObservability: {MemoryMiB: Stat{Mean: obsMiB}, CPUMillicores: Stat{Mean: 50}},
			GroupNetworking:    {MemoryMiB: Stat{Mean: 200}, CPUMillicores: Stat{Mean: 30}},
		}
		return l
	}
	tr := &TrafficRun{Image: "t012-otel:1", CallEveryMs: 100, Traffic: phase(2400, 500), Tracing: phase(2550, 560),
		TracingInstall: Duration(90 * time.Second), Traces: 20}
	var b strings.Builder
	writeTrafficSummary(&b, tr)
	for _, want := range []string{"## HTTP traffic and tracing", "| HTTP traffic | 2400 / 2410 |", "| 200 / 30 |", "| – |",
		"Tracing installed in 1m30s; traced − untraced, node: **+150 MiB mean", "listed 20 traces"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("summary lacks %q:\n%s", want, b.String())
		}
	}
	var none strings.Builder
	writeTrafficSummary(&none, nil)
	if none.Len() != 0 {
		t.Errorf("nil run wrote %q", none.String())
	}
}
