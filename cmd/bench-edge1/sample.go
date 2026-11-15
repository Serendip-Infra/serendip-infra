package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// SampleRun is one phase sampled by `bench-edge1 sample`: the cluster
// only, with no serendipd. Spec 023 T003 uses it to measure observability
// backends installed by hand, against a baseline taken the same way.
type SampleRun struct {
	Label           string         `json:"label"`
	StartedAt       time.Time      `json:"startedAt"`
	Machine         MachineProfile `json:"machine"`
	Simulation      Simulation     `json:"simulation"`
	IntervalSeconds float64        `json:"intervalSeconds"`
	Samples         []Sample       `json:"samples"`
	Cluster         Aggregate      `json:"cluster"`
}

// runSample: bench-edge1 sample --kubeconfig K --label L --out DIR
// [--samples N] [--interval D] [--sim-*] [--disk-probe=false].
func runSample(args []string) error {
	fs := flag.NewFlagSet("bench-edge1 sample", flag.ContinueOnError)
	kubeconfig := fs.String("kubeconfig", "", "kubeconfig of the cluster (required)")
	label := fs.String("label", "", "what this phase is, e.g. \"B: OpenObserve, idle\" (required)")
	out := fs.String("out", "", "output directory for sample.json and summary.md (required)")
	n := fs.Int("samples", 12, "samples to take")
	interval := fs.Duration("interval", 10*time.Second, "time between samples")
	var sim Simulation
	var notSimulated string
	fs.Float64Var(&sim.CPUs, "sim-cpus", 0, "CPU quota applied to simulate reference hardware")
	fs.StringVar(&sim.CPUSet, "sim-cpuset", "", "cpuset applied to simulate reference hardware")
	fs.StringVar(&sim.Note, "sim-note", "", "free-text description of the simulation")
	fs.StringVar(&notSimulated, "not-simulated", "", "semicolon-separated reference-hardware traits the simulation could not reproduce")
	fs.BoolVar(&diskProbe, "disk-probe", true, "measure host-path volumes with a short-lived du pod at the first and last sample")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *kubeconfig == "" || *label == "" || *out == "" || *n < 1 {
		return fmt.Errorf("--kubeconfig, --label, --out and --samples ≥ 1 are required")
	}
	for _, s := range strings.Split(notSimulated, ";") {
		if s = strings.TrimSpace(s); s != "" {
			sim.NotSimulated = append(sim.NotSimulated, s)
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	cl, err := newCluster(*kubeconfig)
	if err != nil {
		return err
	}
	node, err := cl.node(ctx)
	if err != nil {
		return err
	}
	r := SampleRun{
		Label: *label, StartedAt: time.Now().UTC(), Simulation: sim, IntervalSeconds: interval.Seconds(),
		Machine: MachineProfile{
			HostOS: runtime.GOOS + "/" + runtime.GOARCH, HostCPU: hostCPUModel(),
			NodeName: node.Name, NodeKernel: node.Kernel, NodeOSImage: node.OSImage,
			ContainerRuntime: node.Runtime, KubeletVersion: node.Kubelet,
			NodeCPUCapacity: node.CPUCap, NodeMemoryCapacity: node.MemoryCap,
		},
	}
	samples, _, err := samplePhase(ctx, cl, node.Name, nil, *n, *interval)
	r.Samples, r.Cluster = samples, aggregate(samples)
	if werr := writeSampleRun(*out, r); werr != nil {
		return werr
	}
	return err // samples so far are written even if sampling stopped early
}

func writeSampleRun(dir string, r SampleRun) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "sample.json"), raw, 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "summary.md"), []byte(renderSampleRun(r)), 0o644)
}

func renderSampleRun(r SampleRun) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", r.Label)
	if r.Simulation.Active() {
		fmt.Fprintf(&b, "**SIMULATION — does not satisfy the reference-hardware gate.** %s (cpus %.1f, cpuset %s).", r.Simulation.Note, r.Simulation.CPUs, r.Simulation.CPUSet)
		if len(r.Simulation.NotSimulated) > 0 {
			fmt.Fprintf(&b, " Not simulated: %s.", strings.Join(r.Simulation.NotSimulated, "; "))
		}
		b.WriteString("\n\n")
	}
	fmt.Fprintf(&b, "- Started %s, %d samples every %.0fs\n- Node %s (%s CPU, %s memory), %s\n\n",
		r.StartedAt.Format(time.RFC3339), len(r.Samples), r.IntervalSeconds, r.Machine.NodeName, r.Machine.NodeCPUCapacity, r.Machine.NodeMemoryCapacity, r.Machine.KubeletVersion)
	writeGroupTable(&b, "Cluster", r.Cluster, seconds(r.IntervalSeconds))
	return b.String()
}

// Delta is a phase's cost over a baseline, both taken by `sample` on the
// same cluster: the node level first (what a package's stated cost is,
// plan 022 T013), then each group and the largest pod changes.
type Delta struct {
	NodeMemoryMiB    float64            `json:"nodeMemoryMiB"`
	NodeMemoryMaxMiB float64            `json:"nodeMemoryMaxMiB"`
	NodeCPUMilli     float64            `json:"nodeCpuMillicores"`
	NodeCPUMaxMilli  float64            `json:"nodeCpuMaxMillicores"`
	Groups           map[string]float64 `json:"groupMemoryMiB"`
	Pods             []PodDelta         `json:"pods"`
}

// PodDelta is one pod's mean memory change between the two phases.
type PodDelta struct {
	Name    string  `json:"name"`
	DeltaMB float64 `json:"deltaMiB"`
}

func deltaOf(base, phase Aggregate) Delta {
	d := Delta{
		NodeMemoryMiB:    phase.NodeMemoryMiB.Mean - base.NodeMemoryMiB.Mean,
		NodeMemoryMaxMiB: phase.NodeMemoryMiB.Max - base.NodeMemoryMiB.Mean,
		NodeCPUMilli:     phase.NodeCPUMillicores.Mean - base.NodeCPUMillicores.Mean,
		NodeCPUMaxMilli:  phase.NodeCPUMillicores.Max - base.NodeCPUMillicores.Mean,
		Groups:           map[string]float64{},
	}
	for g, a := range phase.Groups {
		d.Groups[g] = a.MemoryMiB.Mean - base.Groups[g].MemoryMiB.Mean
	}
	for g, a := range base.Groups {
		if _, ok := phase.Groups[g]; !ok {
			d.Groups[g] = -a.MemoryMiB.Mean
		}
	}
	pods := map[string]float64{}
	for _, p := range phase.Pods {
		pods[p.Name] += p.MemoryMiB.Mean
	}
	for _, p := range base.Pods {
		pods[p.Name] -= p.MemoryMiB.Mean
	}
	for name, v := range pods {
		d.Pods = append(d.Pods, PodDelta{Name: name, DeltaMB: v})
	}
	sort.Slice(d.Pods, func(i, j int) bool {
		if math.Abs(d.Pods[i].DeltaMB) != math.Abs(d.Pods[j].DeltaMB) {
			return math.Abs(d.Pods[i].DeltaMB) > math.Abs(d.Pods[j].DeltaMB)
		}
		return d.Pods[i].Name < d.Pods[j].Name
	})
	return d
}

func renderDelta(baseLabel, phaseLabel string, d Delta) string {
	var b strings.Builder
	fmt.Fprintf(&b, "### %s over %s\n\n", phaseLabel, baseLabel)
	fmt.Fprintf(&b, "- **Node memory:** %+.0f MiB mean, %+.0f MiB at its max\n", d.NodeMemoryMiB, d.NodeMemoryMaxMiB)
	fmt.Fprintf(&b, "- **Node CPU:** %+.0f mcores mean, %+.0f mcores at its max\n\n", d.NodeCPUMilli, d.NodeCPUMaxMilli)
	b.WriteString("| Group | Memory change (MiB, mean) |\n|---|---|\n")
	groups := map[string]GroupAggregate{}
	for g := range d.Groups {
		groups[g] = GroupAggregate{}
	}
	for _, g := range sortedGroups(groups) {
		if math.Abs(d.Groups[g]) >= 0.5 {
			fmt.Fprintf(&b, "| %s | %+.1f |\n", g, d.Groups[g])
		}
	}
	b.WriteString("\nLargest pod changes:\n\n| Pod | Memory change (MiB, mean) |\n|---|---|\n")
	for i, p := range d.Pods {
		if i == topPods {
			break
		}
		fmt.Fprintf(&b, "| %s | %+.1f |\n", p.Name, p.DeltaMB)
	}
	return b.String()
}

// runDelta: bench-edge1 delta <baseline/sample.json> <phase/sample.json>.
func runDelta(args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("usage: bench-edge1 delta <baseline/sample.json> <phase/sample.json>")
	}
	var rs [2]SampleRun
	for i, p := range args {
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(raw, &rs[i]); err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
	}
	fmt.Print(renderDelta(rs[0].Label, rs[1].Label, deltaOf(rs[0].Cluster, rs[1].Cluster)))
	return nil
}
