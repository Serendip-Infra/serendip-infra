package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	buildplane "github.com/serendip-infra/serendip-infra/providers/build/openchoreo"
)

// For the observability package (specs/023-observability T012) the work
// is plan decision 9's load: a load phase, then a disk phase still under
// load, whose volumes' growth is the package's stated disk use.

// packageKind is what the harness knows about one optional package: the
// capability it provides and the group its own pods are counted in.
type packageKind struct {
	capability, group string
}

var packageKinds = map[string]packageKind{
	"openchoreo-workflow-plane":      {capability: buildCapability, group: GroupBuilds},
	"openchoreo-observability-plane": {capability: observabilityCapability, group: GroupObservability},
}

// kindOf returns provider's kind. An unknown provider is refused when
// the flags are parsed, so the zero value only shows up in a hand-edited
// result.
func kindOf(provider string) packageKind { return packageKinds[provider] }

// The package run (specs/022-builds T013) measures one optional package
// on top of the platform: its install, its idle cost, its peak while it
// works (for builds, while a build runs), its resync cost, and, for
// builds, auto-build's poller. It replaces the light-workload phases.

// packageOptions configures the package run.
type packageOptions struct {
	provider           string
	repo, branch, path string
	idleSamples        int
	idleInterval       time.Duration
	buildInterval      time.Duration
	pollerSamples      int
	fullHelm           bool
	installTimeout     time.Duration
	buildTimeout       time.Duration
	// The observability package's load (plan decision 9).
	loadScript   string
	loadSamples  int
	diskSamples  int
	diskInterval time.Duration
	// The alert rules (specs/024-alerts T011): rules created after the
	// load phase, and samples while they're evaluated.
	rules        int
	rulesSamples int
	// The traffic phases (specs/025-networking T013): HTTP traffic
	// between two Components after the load phase, then, with tracing,
	// the tracing package installed and the same traffic traced.
	traffic        bool
	tracing        bool
	trafficImage   string
	trafficCallMs  int
	trafficSamples int
}

// PackageRun is what the package run measured.
type PackageRun struct {
	Provider string `json:"provider"`
	// Install is create → Ready of the package's ProviderInstance,
	// including its image pulls.
	Install Duration `json:"install"`
	// Idle is the cluster with the package installed and nothing built
	// yet, and SerendipdIdle serendipd over the same window, resyncing the
	// package on the fingerprint path.
	Idle        Aggregate `json:"idle"`
	IdleSamples []Sample  `json:"idleSamples"`
	// IdleInterval is the time between idle samples, so the volumes'
	// growth can be put per day.
	IdleInterval  float64      `json:"idleIntervalSeconds,omitempty"`
	SerendipdIdle ProcessStats `json:"serendipdIdle"`
	// ResyncMillicores is serendipd's idle CPU with the package minus
	// without it, in the same run: the fingerprint path's cost.
	ResyncMillicores float64    `json:"resyncMillicores"`
	Builds           []BuildRun `json:"builds"`
	Poller           *PollerRun `json:"poller,omitempty"`
	FullHelm         *HelmRun   `json:"fullHelm,omitempty"`
	// Load and Disk are the observability package's phases under load:
	// Load for the peak, Disk (still under load, longer) for the volumes'
	// growth. LoadQueries is the load driver's own account of its queries.
	Load        *LoadRun `json:"load,omitempty"`
	Disk        *LoadRun `json:"disk,omitempty"`
	LoadQueries string   `json:"loadQueries,omitempty"`
	// Rules is the alert rules' phases, between the load and disk
	// phases; the disk phase then runs with the rules on.
	Rules *RulesRun `json:"rules,omitempty"`
	// Traffic is the HTTP traffic and tracing phases, after the load
	// phase and before the rules'.
	Traffic *TrafficRun `json:"traffic,omitempty"`
	// Incomplete is why the load or disk phase stopped short, if it did.
	Incomplete string `json:"incomplete,omitempty"`
	// Cost is the package's stated cost, derived from the above.
	Cost PackageCost `json:"cost"`
}

// BuildRun is one build, sampled while it ran.
type BuildRun struct {
	Label    string   `json:"label"`
	ID       string   `json:"id"`
	Phase    string   `json:"phase"`
	Commit   string   `json:"commit"`
	Duration Duration `json:"duration"`
	// Running is the build's startedAt → finishedAt, as Serendip reports
	// it.
	Running   Duration     `json:"running"`
	Samples   []Sample     `json:"samples"`
	Cluster   Aggregate    `json:"cluster"`
	Serendipd ProcessStats `json:"serendipd"`
}

// PollerRun is auto-build switched on, with the cluster idle after the
// builds.
type PollerRun struct {
	Seconds   float64      `json:"seconds"`
	Condition string       `json:"autoBuildCondition"`
	Cluster   Aggregate    `json:"cluster"`
	Samples   []Sample     `json:"samples"`
	Serendipd ProcessStats `json:"serendipd"`
	// DeltaMillicores is serendipd's CPU with auto-build on minus the
	// package-idle window's.
	DeltaMillicores float64 `json:"deltaMillicores"`
}

// LoadRun is one phase sampled under load.
type LoadRun struct {
	Seconds         float64      `json:"seconds"`
	IntervalSeconds float64      `json:"intervalSeconds"`
	Cluster         Aggregate    `json:"cluster"`
	Samples         []Sample     `json:"samples"`
	Serendipd       ProcessStats `json:"serendipd"`
	// Writes is the store's change over the phase, when it was read.
	Writes *StoreWrites `json:"writes,omitempty"`
}

// HelmRun is the package's full Helm command run once by itself, the cost
// every resync would pay without the fingerprint.
type HelmRun struct {
	Wall Duration `json:"wall"`
	// HelmCPUSeconds is the helm process's own user+system time.
	HelmCPUSeconds float64   `json:"helmCpuSeconds"`
	Cluster        Aggregate `json:"cluster"`
	Samples        []Sample  `json:"samples"`
	Error          string    `json:"error,omitempty"`
}

// CostFigures are idle and peak memory and CPU.
type CostFigures struct {
	IdleMemoryMiB     int `json:"idleMemoryMiB"`
	IdleCPUMillicores int `json:"idleCPUMillicores"`
	PeakMemoryMiB     int `json:"peakMemoryMiB"`
	PeakCPUMillicores int `json:"peakCPUMillicores"`
}

// PackageCost is what goes into the package's Requirements: what the
// node uses beyond the same run's platform phase. Own is the package's
// own pods alone, for the breakdown: T013 found the package also grew
// kube-apiserver and cert-manager by ~600 MiB (RP-046), which only the
// node total sees.
type PackageCost struct {
	CostFigures
	Own CostFigures `json:"own"`
	// Disk is the package's volumes' growth in the disk phase, put per
	// day, and their capacity: what Requirements.Disk states, with the
	// retention the installer sets.
	Disk *DiskCost `json:"disk,omitempty"`
}

// DiskCost is the package's volumes in the disk phase.
type DiskCost struct {
	GrowthMiB       float64 `json:"growthMiB"`
	SpanSeconds     float64 `json:"spanSeconds"`
	GrowthMiBPerDay int     `json:"growthMiBPerDay"`
	CapacityMiB     int     `json:"capacityMiB"`
	Volumes         int     `json:"volumes"`
}

// measurePackage runs the package phases after the platform's idle
// sample, filling res.Package as it goes.
func measurePackage(ctx context.Context, o options, cl *cluster, nodeName string, d *daemon, a api, res *Result, created *workload) error {
	p := &PackageRun{Provider: o.pkg.provider}
	res.Package = p
	po := o.pkg

	log.Printf("package: installing %s", po.provider)
	began := time.Now()
	inst, err := a.do(ctx, http.MethodPost, "/v1/provider-instances", map[string]any{
		"metadata": map[string]any{"name": po.provider},
		"spec":     map[string]any{"capabilityId": kindOf(po.provider).capability, "providerId": po.provider},
	})
	if err != nil {
		return fmt.Errorf("install %s: %w", po.provider, err)
	}
	instPath := "/v1/provider-instances/" + idOf(inst)
	if err := a.waitTrue(ctx, instPath, "Ready", po.installTimeout); err != nil {
		return fmt.Errorf("install %s: %w", po.provider, err)
	}
	p.Install = Duration(time.Since(began))
	log.Printf("package: Ready after %s", time.Duration(p.Install).Round(time.Second))
	if err := cl.waitAllPodsReady(ctx, 10*time.Minute); err != nil {
		return err
	}
	log.Printf("package: settling %s", o.settle)
	if err := sleep(ctx, o.settle); err != nil {
		return err
	}

	log.Printf("package: idle, %d samples every %s", po.idleSamples, po.idleInterval)
	samples, procs, err := samplePhase(ctx, cl, nodeName, d, po.idleSamples, po.idleInterval)
	p.IdleSamples, p.Idle, p.SerendipdIdle = samples, aggregate(samples), processStatsOf(procs)
	p.IdleInterval = po.idleInterval.Seconds()
	if err != nil {
		return fmt.Errorf("package idle sampling: %w", err)
	}
	p.ResyncMillicores = p.SerendipdIdle.CPUMillicores.Mean - res.Serendipd.CPUMillicores.Mean

	if kindOf(po.provider).capability == observabilityCapability {
		lerr := measureLoad(ctx, o, cl, nodeName, d, a, p, created)
		if lerr != nil {
			// The phases stop short, so their peak and growth aren't the
			// package's: the summary says so, rather than a cost to copy.
			p.Incomplete = lerr.Error()
		}
		p.Cost = packageCost(*p, res.Platform)
		return lerr
	}

	// A source Component starts its first build when it's created.
	w, comp, err := a.createSourceComponent(ctx, po)
	*created = w
	if err != nil {
		return err
	}
	first, err := a.waitFirstBuild(ctx, comp, 5*time.Minute)
	if err != nil {
		return err
	}
	for i, label := range []string{"first (cold node image cache)", "second (warm node image cache)"} {
		id := first
		if i > 0 {
			obj, err := a.do(ctx, http.MethodPost, "/v1/components/"+comp+"/builds", map[string]any{})
			if err != nil {
				return fmt.Errorf("trigger build: %w", err)
			}
			id = idOf(obj)
		}
		log.Printf("package: build %s, sampling every %s", label, po.buildInterval)
		b, err := a.sampleBuild(ctx, cl, nodeName, d, id, label, po.buildInterval, po.buildTimeout)
		p.Builds = append(p.Builds, b)
		if err != nil {
			return err
		}
		log.Printf("package: build %s %s in %s", id, b.Phase, time.Duration(b.Duration).Round(time.Second))
		if b.Phase != "Succeeded" {
			res.Findings = append(res.Findings, fmt.Sprintf("build %s (%s) ended %s", id, label, b.Phase))
		}
	}

	if po.pollerSamples > 0 {
		if err := a.setAutoBuild(ctx, comp, true); err != nil {
			return err
		}
		log.Printf("package: auto-build on, %d samples every %s", po.pollerSamples, po.idleInterval)
		start := time.Now()
		samples, procs, err := samplePhase(ctx, cl, nodeName, d, po.pollerSamples, po.idleInterval)
		pr := &PollerRun{Seconds: time.Since(start).Seconds(), Samples: samples, Cluster: aggregate(samples), Serendipd: processStatsOf(procs)}
		pr.DeltaMillicores = pr.Serendipd.CPUMillicores.Mean - p.SerendipdIdle.CPUMillicores.Mean
		if obj, err := a.do(ctx, http.MethodGet, "/v1/components/"+comp, nil); err == nil {
			pr.Condition = conditionReason(obj, "AutoBuild")
		}
		p.Poller = pr
		if err != nil {
			return fmt.Errorf("poller sampling: %w", err)
		}
		if err := a.setAutoBuild(ctx, comp, false); err != nil {
			return err
		}
	}

	if po.fullHelm {
		log.Printf("package: one full Helm run, for comparison with the fingerprint path")
		p.FullHelm = runHelmOnce(ctx, cl, nodeName, o.kubeconfigAbs, 3*time.Second)
	}

	p.Cost = packageCost(*p, res.Platform)
	return nil
}

// buildCapability and observabilityCapability are the capabilities the
// two packages provide.
const (
	buildCapability         = "build"
	observabilityCapability = "observability"
)

// loadFunc runs one of the load script's verbs (start, stop, latency).
type loadFunc func(ctx context.Context, verb string) (string, error)

// sampleFunc samples the cluster and serendipd n times, interval apart.
type sampleFunc func(ctx context.Context, n int, interval time.Duration) ([]Sample, []procSample, error)

// phaseFunc samples one named phase under load.
type phaseFunc func(ctx context.Context, name string, n int, interval time.Duration) (*LoadRun, error)

// betweenFunc runs between the load and disk phases (the rules phases),
// sampling with phase.
type betweenFunc func(ctx context.Context, phase phaseFunc) error

// writesFunc reads the store, so a phase can state its writes.
type writesFunc func(ctx context.Context) (storeCounts, error)

// measureLoad starts decision 9's load, samples a load phase, the rules
// phases if there are rules, and then a longer disk phase under the same
// load, and stops the load.
func measureLoad(ctx context.Context, o options, cl *cluster, nodeName string, d *daemon, a api, p *PackageRun, created *workload) error {
	po := o.pkg
	load := func(ctx context.Context, verb string) (string, error) {
		cmd := exec.CommandContext(ctx, "bash", po.loadScript, verb)
		// The kubeconfig goes in KUBECONFIG, not in KUBECTL's words, so a
		// path with spaces survives load.sh's unquoted $k.
		cmd.Env = append(os.Environ(), "KUBECTL=kubectl", "KUBECONFIG="+o.kubeconfigAbs)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return string(out), fmt.Errorf("load.sh %s: %w: %s", verb, err, strings.TrimSpace(string(out)))
		}
		return string(out), nil
	}
	sample := func(ctx context.Context, n int, interval time.Duration) ([]Sample, []procSample, error) {
		return samplePhase(ctx, cl, nodeName, d, n, interval)
	}
	stopHint := fmt.Sprintf("KUBECONFIG=%s bash %s stop", o.kubeconfigAbs, po.loadScript)
	writes := func(ctx context.Context) (storeCounts, error) { return readStoreCounts(ctx, o.dbPath) }
	var between betweenFunc
	var tw trafficWorkload
	if po.traffic || po.rules > 0 {
		between = func(ctx context.Context, phase phaseFunc) error {
			if po.traffic {
				if err := measureTraffic(ctx, o, cl, a, phase, p, &tw); err != nil {
					return err
				}
			}
			if po.rules > 0 {
				return measureRules(ctx, o, cl, a, d, phase, p, created)
			}
			return nil
		}
	}
	err := runLoad(ctx, po, load, sample, stopHint, p, between, writes)
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancel()
	finishRules(fctx, a, d, p.Rules)
	if tw.project != "" {
		if derr := a.deleteWorkload(fctx, tw.workload); derr != nil {
			log.Printf("traffic: deleting the pair: %v", derr)
		}
	}
	return err
}

// runLoad is measureLoad's sequence, with the script and the sampler
// passed in so a test can check the load is always stopped. stopHint is
// the command that stops the load by hand, logged if stop fails.
func runLoad(ctx context.Context, po packageOptions, load loadFunc, sample sampleFunc, stopHint string, p *PackageRun, between betweenFunc, writes writesFunc) error {
	log.Printf("package: starting the load (%s)", po.loadScript)
	// Stopped whatever happens, even if start failed partway, on a
	// context a cancelled run doesn't end, so the loggers don't keep
	// writing, but with a limit, so a wedged cluster can't hold the
	// harness open.
	defer func() {
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
		defer cancel()
		if _, err := load(sctx, "stop"); err != nil {
			log.Printf("package: %v; the load may still be running: %s", err, stopHint)
		}
	}()
	if _, err := load(ctx, "start"); err != nil {
		return err
	}
	phase := func(ctx context.Context, name string, n int, interval time.Duration) (*LoadRun, error) {
		log.Printf("package: %s, %d samples every %s", name, n, interval)
		var before storeCounts
		var werr error
		if writes != nil {
			before, werr = writes(ctx)
		}
		start := time.Now()
		samples, procs, err := sample(ctx, n, interval)
		l := &LoadRun{
			Seconds: time.Since(start).Seconds(), IntervalSeconds: interval.Seconds(),
			Samples: samples, Cluster: aggregate(samples), Serendipd: processStatsOf(procs),
		}
		if writes != nil && werr == nil {
			var after storeCounts
			if after, werr = writes(ctx); werr == nil {
				l.Writes = writesBetween(before, after)
			}
		}
		if werr != nil {
			log.Printf("package: %s: reading the store: %v", name, werr)
		}
		return l, err
	}
	var err error
	if p.Load, err = phase(ctx, "under load", po.loadSamples, po.idleInterval); err != nil {
		return fmt.Errorf("load sampling: %w", err)
	}
	if between != nil {
		if err := between(ctx, phase); err != nil {
			return err
		}
	}
	if po.diskSamples > 0 {
		name := "disk, still under load"
		if between != nil {
			name += ", rules on"
		}
		if p.Disk, err = phase(ctx, name, po.diskSamples, po.diskInterval); err != nil {
			return fmt.Errorf("disk sampling: %w", err)
		}
	}
	if out, err := load(ctx, "latency"); err == nil {
		p.LoadQueries = strings.TrimSpace(out)
	} else {
		log.Printf("package: %v", err)
	}
	return nil
}

// packageCost states the package's cost from the run, rounded up so the
// stated figure is never below what was measured.
//   - Idle is the larger of the mean before any build and after the
//     builds (the controller grows a little once it has run builds).
//   - Peak is the largest sample while a build ran.
//   - The stated figures are the node's use beyond platform (the same
//     run's idle platform, without the package), never below the
//     package's own pods.
func packageCost(p PackageRun, platform Aggregate) PackageCost {
	group := kindOf(p.Provider).group
	own := func(a Aggregate) GroupAggregate { return a.Groups[group] }
	idleMem, idleCPU := own(p.Idle).MemoryMiB.Mean, own(p.Idle).CPUMillicores.Mean
	nodeIdleMem, nodeIdleCPU := p.Idle.NodeMemoryMiB.Mean, p.Idle.NodeCPUMillicores.Mean
	if p.Poller != nil {
		idleMem = math.Max(idleMem, own(p.Poller.Cluster).MemoryMiB.Mean)
		idleCPU = math.Max(idleCPU, own(p.Poller.Cluster).CPUMillicores.Mean)
		nodeIdleMem = math.Max(nodeIdleMem, p.Poller.Cluster.NodeMemoryMiB.Mean)
		nodeIdleCPU = math.Max(nodeIdleCPU, p.Poller.Cluster.NodeCPUMillicores.Mean)
	}
	var peakMem, peakCPU, nodePeakMem, nodePeakCPU float64
	var working []Aggregate
	for _, b := range p.Builds {
		working = append(working, b.Cluster)
	}
	for _, l := range []*LoadRun{p.Load, p.Disk} {
		if l != nil {
			working = append(working, l.Cluster)
		}
	}
	for _, a := range working {
		peakMem = math.Max(peakMem, own(a).MemoryMiB.Max)
		peakCPU = math.Max(peakCPU, own(a).CPUMillicores.Max)
		nodePeakMem = math.Max(nodePeakMem, a.NodeMemoryMiB.Max)
		nodePeakCPU = math.Max(nodePeakCPU, a.NodeCPUMillicores.Max)
	}
	figures := func(idleMem, idleCPU, peakMem, peakCPU float64) CostFigures {
		return CostFigures{
			IdleMemoryMiB: int(math.Ceil(idleMem)), IdleCPUMillicores: int(math.Ceil(idleCPU)),
			PeakMemoryMiB: int(math.Ceil(math.Max(peakMem, idleMem))), PeakCPUMillicores: int(math.Ceil(math.Max(peakCPU, idleCPU))),
		}
	}
	beyond := func(v, base, floor float64) float64 { return math.Max(v-base, floor) }
	baseMem, baseCPU := platform.NodeMemoryMiB.Mean, platform.NodeCPUMillicores.Mean
	return PackageCost{
		CostFigures: figures(
			beyond(nodeIdleMem, baseMem, idleMem), beyond(nodeIdleCPU, baseCPU, idleCPU),
			beyond(nodePeakMem, baseMem, peakMem), beyond(nodePeakCPU, baseCPU, peakCPU)),
		Own:  figures(idleMem, idleCPU, peakMem, peakCPU),
		Disk: diskCost(p.Disk, group),
	}
}

// diskCost sums the growth of group's volumes over the disk phase, put
// per day (rounded up), and their capacity. It's nil without a disk phase,
// or without volumes measured at both its ends.
func diskCost(l *LoadRun, group string) *DiskCost {
	if l == nil {
		return nil
	}
	c := &DiskCost{}
	var capacity float64
	interval := time.Duration(l.IntervalSeconds * float64(time.Second))
	for _, v := range l.Cluster.Volumes {
		if v.Group != group || v.Intervals == 0 {
			continue
		}
		c.Volumes++
		c.GrowthMiB += v.LastUsedMiB - v.FirstUsedMiB
		c.SpanSeconds = math.Max(c.SpanSeconds, v.Span(interval).Seconds())
		capacity += v.CapacityMiB
	}
	if c.Volumes == 0 || c.SpanSeconds == 0 {
		return nil
	}
	// A compaction can shrink the stores; that isn't negative growth.
	c.GrowthMiB = math.Max(c.GrowthMiB, 0)
	c.GrowthMiBPerDay = int(math.Ceil(c.GrowthMiB * 86400 / c.SpanSeconds))
	c.CapacityMiB = int(math.Round(capacity))
	return c
}

// createSourceComponent makes a Project, an Application in it and a
// Component built from po's repository.
func (a api) createSourceComponent(ctx context.Context, po packageOptions) (workload, string, error) {
	var w workload
	create := func(path string, body map[string]any) (string, error) {
		obj, err := a.do(ctx, http.MethodPost, path, body)
		if err != nil {
			return "", err
		}
		return idOf(obj), nil
	}
	owned := func(name, kind, id string) map[string]any {
		return map[string]any{"name": name, "ownerRefs": []any{map[string]any{"kind": kind, "id": id}}}
	}
	var err error
	if w.project, err = create("/v1/projects", map[string]any{"metadata": map[string]any{"name": "bench"}}); err != nil {
		return w, "", err
	}
	if w.app, err = create("/v1/applications", map[string]any{"metadata": owned("bench", "Project", w.project)}); err != nil {
		return w, "", err
	}
	if err := a.waitReason(ctx, "/v1/applications/"+w.app, "Ready", "Deployed", 5*time.Minute); err != nil {
		return w, "", err
	}
	comp, err := create("/v1/components", map[string]any{
		"metadata": owned("bench-built", "Application", w.app),
		"spec": map[string]any{"source": map[string]any{
			"repository": po.repo, "branch": po.branch, "path": po.path,
		}},
	})
	return w, comp, err
}

// list GETs a list endpoint, which answers a JSON array.
func (a api) list(ctx context.Context, path string) ([]map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.base+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := a.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", path, resp.Status)
	}
	var out []map[string]any
	return out, json.NewDecoder(resp.Body).Decode(&out)
}

func (a api) waitFirstBuild(ctx context.Context, comp string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for {
		bs, err := a.list(ctx, "/v1/components/"+comp+"/builds")
		if err == nil && len(bs) > 0 {
			return idOf(bs[len(bs)-1]), nil
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("no build of component %s after %s (last error %v)", comp, timeout, err)
		}
		if err := sleep(ctx, 2*time.Second); err != nil {
			return "", err
		}
	}
}

func (a api) waitTrue(ctx context.Context, path, typ string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		obj, err := a.do(ctx, http.MethodGet, path, nil)
		if err == nil && conditionTrue(obj, typ) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s: %s not True after %s (reason %q)", path, typ, timeout, conditionReason(obj, typ))
		}
		if err := sleep(ctx, 2*time.Second); err != nil {
			return err
		}
	}
}

// sampleBuild samples the cluster and serendipd every interval until the
// build ends.
func (a api) sampleBuild(ctx context.Context, cl *cluster, nodeName string, d *daemon, id, label string, interval, timeout time.Duration) (b BuildRun, err error) {
	b = BuildRun{Label: label, ID: id}
	began := time.Now()
	var procs []procSample
	defer func() {
		b.Duration = Duration(time.Since(began))
		b.Cluster = aggregate(b.Samples)
		b.Serendipd = processStatsOf(procs)
	}()
	for {
		if s, err := cl.summary(ctx, nodeName); err == nil {
			b.Samples = append(b.Samples, sampleFrom(s))
		}
		if p, err := readProcess(d.pid()); err == nil {
			procs = append(procs, p)
		}
		obj, err := a.do(ctx, http.MethodGet, "/v1/builds/"+id, nil)
		if err == nil {
			st, _ := obj["status"].(map[string]any)
			b.Phase, _ = st["phase"].(string)
			b.Commit, _ = st["commit"].(string)
			if b.Phase == "Succeeded" || b.Phase == "Failed" || b.Phase == "Cancelled" {
				b.Running = Duration(elapsedBetween(st["startedAt"], st["finishedAt"]))
				return b, nil
			}
		}
		if time.Since(began) > timeout {
			return b, fmt.Errorf("build %s still %q after %s", id, b.Phase, timeout)
		}
		if err := sleep(ctx, interval); err != nil {
			return b, err
		}
	}
}

func elapsedBetween(from, to any) time.Duration {
	f, _ := from.(string)
	t, _ := to.(string)
	a, err1 := time.Parse(time.RFC3339Nano, f)
	b, err2 := time.Parse(time.RFC3339Nano, t)
	if err1 != nil || err2 != nil {
		return 0
	}
	return b.Sub(a)
}

// setAutoBuild turns auto-build on or off with a full PUT of the
// Component.
func (a api) setAutoBuild(ctx context.Context, comp string, on bool) error {
	obj, err := a.do(ctx, http.MethodGet, "/v1/components/"+comp, nil)
	if err != nil {
		return err
	}
	spec, _ := obj["spec"].(map[string]any)
	src, _ := spec["source"].(map[string]any)
	if src == nil {
		return fmt.Errorf("component %s has no source", comp)
	}
	src["autoBuild"] = on
	delete(obj, "status")
	_, err = a.do(ctx, http.MethodPut, "/v1/components/"+comp, obj)
	return err
}

// runHelmOnce runs the package's full Helm command by itself, sampling the
// cluster while it runs.
func runHelmOnce(ctx context.Context, cl *cluster, nodeName, kubeconfig string, interval time.Duration) *HelmRun {
	h := &HelmRun{}
	args := append(buildplane.HelmInstallArgs(), "--kubeconfig", kubeconfig)
	cmd := exec.CommandContext(ctx, "helm", args...)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	began := time.Now()
	if err := cmd.Start(); err != nil {
		h.Error = err.Error()
		return h
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var waitErr error
loop:
	for {
		if s, err := cl.summary(ctx, nodeName); err == nil {
			h.Samples = append(h.Samples, sampleFrom(s))
		}
		select {
		case waitErr = <-done:
			break loop
		case <-time.After(interval):
		}
	}
	h.Wall = Duration(time.Since(began))
	if cmd.ProcessState != nil {
		h.HelmCPUSeconds = (cmd.ProcessState.UserTime() + cmd.ProcessState.SystemTime()).Seconds()
	}
	if waitErr != nil {
		h.Error = waitErr.Error()
	}
	h.Cluster = aggregate(h.Samples)
	return h
}

// writePackageSummary renders the package run's section of summary.md.
func writePackageSummary(b *strings.Builder, r Result) {
	p := r.Package
	fmt.Fprintf(b, "\n## Package: %s\n\n", p.Provider)
	fmt.Fprintf(b, "Install (create → Ready, with image pulls): %s\n\n", fmtDur(p.Install))
	writeGroupTable(b, fmt.Sprintf("Installed, idle (%d samples)", len(p.IdleSamples)), p.Idle, seconds(p.IdleInterval))
	fmt.Fprintf(b, "\nserendipd with the package idle: %.1f MiB RSS, %.2f mcores mean (without the package, same run: %.1f MiB, %.2f mcores). Resync on the fingerprint path: **%+.2f mcores**.\n",
		p.SerendipdIdle.RSSMiB.Mean, p.SerendipdIdle.CPUMillicores.Mean, r.Serendipd.RSSMiB.Mean, r.Serendipd.CPUMillicores.Mean, p.ResyncMillicores)
	group := kindOf(p.Provider).group
	for _, bd := range p.Builds {
		g := bd.Cluster.Groups[group]
		fmt.Fprintf(b, "\n### Build: %s\n\n%s, commit `%.12s`, %s seen by the harness (%s running). %d samples.\n\n", bd.Label, bd.Phase, bd.Commit, fmtDur(bd.Duration), fmtDur(bd.Running), len(bd.Samples))
		fmt.Fprintf(b, "| | Mean | Max |\n|---|---|---|\n| Builds group memory (MiB) | %.1f | %.1f |\n| Builds group CPU (mcores) | %.1f | %.1f |\n| Node memory (MiB) | %.1f | %.1f |\n| Node CPU (mcores) | %.1f | %.1f |\n| serendipd CPU (mcores) | %.2f | %.2f |\n",
			g.MemoryMiB.Mean, g.MemoryMiB.Max, g.CPUMillicores.Mean, g.CPUMillicores.Max,
			bd.Cluster.NodeMemoryMiB.Mean, bd.Cluster.NodeMemoryMiB.Max, bd.Cluster.NodeCPUMillicores.Mean, bd.Cluster.NodeCPUMillicores.Max,
			bd.Serendipd.CPUMillicores.Mean, bd.Serendipd.CPUMillicores.Max)
	}
	for _, l := range []struct {
		title string
		run   *LoadRun
	}{{"Under load", p.Load}, {"Disk phase, still under load", p.Disk}} {
		if l.run == nil {
			continue
		}
		b.WriteString("\n")
		writeGroupTable(b, fmt.Sprintf("%s (%d samples over %.0f min)", l.title, len(l.run.Samples), l.run.Seconds/60), l.run.Cluster, seconds(l.run.IntervalSeconds))
		fmt.Fprintf(b, "\nserendipd: %.1f MiB RSS, %.2f mcores mean.\n", l.run.Serendipd.RSSMiB.Mean, l.run.Serendipd.CPUMillicores.Mean)
	}
	writeTrafficSummary(b, p.Traffic)
	writeRulesSummary(b, p.Rules)
	if p.LoadQueries != "" {
		fmt.Fprintf(b, "\nThe load driver's queries:\n\n```\n%s\n```\n", p.LoadQueries)
	}
	if pr := p.Poller; pr != nil {
		g := pr.Cluster.Groups[group]
		fmt.Fprintf(b, "\n### Auto-build on (%.0f s)\n\nAutoBuild condition: %s. serendipd %.2f mcores mean (%+.2f against package idle). Builds group after the builds: %.1f MiB, %.1f mcores.\n",
			pr.Seconds, pr.Condition, pr.Serendipd.CPUMillicores.Mean, pr.DeltaMillicores, g.MemoryMiB.Mean, g.CPUMillicores.Mean)
	}
	if h := p.FullHelm; h != nil {
		fmt.Fprintf(b, "\n### One full Helm run (what each resync would cost without the fingerprint)\n\n%s wall, helm's own CPU %.2f s, node CPU %.0f mcores mean / %.0f max over %d samples.",
			fmtDur(h.Wall), h.HelmCPUSeconds, h.Cluster.NodeCPUMillicores.Mean, h.Cluster.NodeCPUMillicores.Max, len(h.Samples))
		if h.Error != "" {
			fmt.Fprintf(b, " Error: %s.", h.Error)
		}
		b.WriteString("\n")
	}
	c := p.Cost
	if p.Incomplete != "" {
		fmt.Fprintf(b, "\n> **Incomplete run, don't state these figures:** %s.\n", p.Incomplete)
	}
	fmt.Fprintf(b, "\n### Stated cost (Requirements)\n\nThe node's use beyond the platform phase above (%.0f MiB, %.0f mcores mean), never below the package's own pods.\n\n| | Idle memory | Idle CPU | Peak memory | Peak CPU |\n|---|---|---|---|---|\n| **Stated (node)** | %d MiB | %d mcores | %d MiB | %d mcores |\n| Package's own pods | %d MiB | %d mcores | %d MiB | %d mcores |\n",
		r.Platform.NodeMemoryMiB.Mean, r.Platform.NodeCPUMillicores.Mean,
		c.IdleMemoryMiB, c.IdleCPUMillicores, c.PeakMemoryMiB, c.PeakCPUMillicores,
		c.Own.IdleMemoryMiB, c.Own.IdleCPUMillicores, c.Own.PeakMemoryMiB, c.Own.PeakCPUMillicores)
	if dc := c.Disk; dc != nil {
		fmt.Fprintf(b, "\nDisk: the package's %d volumes grew %.1f MiB over %.0f min of the disk phase, **%d MiB a day** extrapolated, of %d MiB capacity.\n",
			dc.Volumes, dc.GrowthMiB, dc.SpanSeconds/60, dc.GrowthMiBPerDay, dc.CapacityMiB)
	}
}

func fmtDur(d Duration) string { return time.Duration(d).Round(time.Second).String() }
