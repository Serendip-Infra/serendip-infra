package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// The traffic phases (specs/025-networking T013) measure what HTTP
// traffic between Components costs with the CNI in place, and then what
// the tracing package adds, on top of the observability package and under
// spec 023's log load:
//  1. Traffic: a caller Component calling a server Component (both an
//     OpenTelemetry-instrumented image), tracing not installed. On Cilium,
//     Hubble sees each request (request visibility), and the observability
//     package's Prometheus scrapes Hubble's request series.
//  2. Tracing: the tracing package installed, both Components given a new
//     release so they send their spans to its collector (plan decision 11),
//     and the same traffic sampled again.
// The disk phase that follows, when there is one, still runs with them.

// tracingProvider and tracingCapability are the tracing package's.
const (
	tracingProvider   = "openchoreo-tracing"
	tracingCapability = "tracing"
	trafficPort       = 8080
)

// TrafficRun is what the traffic phases measured.
type TrafficRun struct {
	Image string `json:"image"`
	// CallEveryMs is the caller's period: each call is one request to the
	// server and, with tracing, a trace of three spans.
	CallEveryMs int      `json:"callEveryMs"`
	Traffic     *LoadRun `json:"traffic,omitempty"`
	// TracingInstall is create → Ready of the tracing package.
	TracingInstall Duration `json:"tracingInstall,omitempty"`
	Tracing        *LoadRun `json:"tracing,omitempty"`
	// Traces is how many traces the server's Deployment listed at the end
	// of the tracing phase (capped by the API's limit), the check that
	// spans were arriving.
	Traces int `json:"traces"`
	// TracesError is why the list failed, if it did.
	TracesError string `json:"tracesError,omitempty"`
}

// trafficWorkload is the pair's resources, removed at the end.
type trafficWorkload struct {
	workload
	server, caller       string
	serverDep, callerDep string
}

// measureTraffic runs the traffic phases. tw records what was created.
func measureTraffic(ctx context.Context, o options, cl *cluster, a api, phase phaseFunc, p *PackageRun, tw *trafficWorkload) error {
	po := o.pkg
	t := &TrafficRun{Image: po.trafficImage, CallEveryMs: po.trafficCallMs}
	p.Traffic = t

	log.Printf("traffic: deploying a caller and a server Component (%s, a call every %d ms)", po.trafficImage, po.trafficCallMs)
	if err := a.deployPair(ctx, po, tw); err != nil {
		return fmt.Errorf("traffic: deploy: %w", err)
	}
	if err := cl.waitAllPodsReady(ctx, 10*time.Minute); err != nil {
		return err
	}
	if err := sleep(ctx, o.settle); err != nil {
		return err
	}
	var err error
	if t.Traffic, err = phase(ctx, "HTTP traffic", po.trafficSamples, po.idleInterval); err != nil {
		return err
	}
	if !po.tracing {
		return nil
	}

	log.Printf("traffic: installing %s", tracingProvider)
	began := time.Now()
	inst, err := a.do(ctx, http.MethodPost, "/v1/provider-instances", map[string]any{
		"metadata": map[string]any{"name": tracingProvider},
		"spec":     map[string]any{"capabilityId": tracingCapability, "providerId": tracingProvider},
	})
	if err != nil {
		return fmt.Errorf("install %s: %w", tracingProvider, err)
	}
	if err := a.waitTrue(ctx, "/v1/provider-instances/"+idOf(inst), "Ready", po.installTimeout); err != nil {
		return fmt.Errorf("install %s: %w", tracingProvider, err)
	}
	t.TracingInstall = Duration(time.Since(began))
	log.Printf("traffic: tracing Ready after %s; giving both Components a new release", time.Duration(t.TracingInstall).Round(time.Second))
	// The trace variables join a Component's next new release only, so
	// a changed variable makes one.
	for _, c := range []string{tw.server, tw.caller} {
		if err := a.setEnv(ctx, c, "BENCH_TRACED", "1"); err != nil {
			return fmt.Errorf("traffic: new release: %w", err)
		}
	}
	if err := a.waitTraced(ctx, cl, []string{tw.serverDep, tw.callerDep}, 10*time.Minute); err != nil {
		return err
	}
	if err := cl.waitAllPodsReady(ctx, 10*time.Minute); err != nil {
		return err
	}
	if err := sleep(ctx, o.settle); err != nil {
		return err
	}
	if t.Tracing, err = phase(ctx, "HTTP traffic, traced", po.trafficSamples, po.idleInterval); err != nil {
		return err
	}
	page, err := a.do(ctx, http.MethodGet, "/v1/deployments/"+tw.serverDep+"/traces?from="+time.Now().Add(-5*time.Minute).UTC().Format(time.RFC3339), nil)
	if err != nil {
		t.TracesError = err.Error()
	}
	traces, _ := page["traces"].([]any)
	t.Traces = len(traces)
	return nil
}

// deployPair creates a Project, Environment and Application, a server
// Component and a caller Component that depends on it (the caller gets
// the server's URL in TARGET), and a Deployment of each.
func (a api) deployPair(ctx context.Context, po packageOptions, tw *trafficWorkload) error {
	create := func(path string, body map[string]any) (string, error) {
		obj, err := a.do(ctx, http.MethodPost, path, body)
		if err != nil {
			return "", err
		}
		if id := idOf(obj); id != "" {
			return id, nil
		}
		return "", fmt.Errorf("POST %s: response carried no metadata.id", path)
	}
	owned := func(name, kind, id string) map[string]any {
		return map[string]any{"name": name, "ownerRefs": []any{map[string]any{"kind": kind, "id": id}}}
	}
	var err error
	if tw.project, err = create("/v1/projects", map[string]any{"metadata": map[string]any{"name": "bench-traffic"}}); err != nil {
		return err
	}
	if tw.env, err = create("/v1/environments", map[string]any{"metadata": owned("bench", "Project", tw.project)}); err != nil {
		return err
	}
	if tw.app, err = create("/v1/applications", map[string]any{"metadata": owned("traffic", "Project", tw.project)}); err != nil {
		return err
	}
	if err := a.waitReason(ctx, "/v1/applications/"+tw.app, "Ready", "Deployed", 5*time.Minute); err != nil {
		return err
	}
	port := strconv.Itoa(trafficPort)
	ports := []any{map[string]any{"name": "http", "port": trafficPort}}
	// The server's port is exposed: request metrics come from an exposed
	// port's L7 visibility (RP-082).
	exposed := []any{map[string]any{"name": "http", "port": trafficPort, "expose": true}}
	if tw.server, err = create("/v1/components", map[string]any{
		"metadata": owned("server", "Application", tw.app),
		"spec":     map[string]any{"image": po.trafficImage, "ports": exposed, "env": map[string]any{"PORT": port}},
	}); err != nil {
		return err
	}
	if tw.caller, err = create("/v1/components", map[string]any{
		"metadata": owned("caller", "Application", tw.app),
		"spec": map[string]any{
			"image": po.trafficImage, "ports": ports,
			"env":          map[string]any{"PORT": port, "CALL_EVERY_MS": strconv.Itoa(po.trafficCallMs)},
			"dependencies": []any{map[string]any{"component": tw.server, "port": "http", "env": "TARGET"}},
		},
	}); err != nil {
		return err
	}
	for _, c := range []struct {
		name, comp string
		dep        *string
	}{{"server", tw.server, &tw.serverDep}, {"caller", tw.caller, &tw.callerDep}} {
		if err := a.waitTrueFor(ctx, "/v1/components/"+c.comp, "Ready", 5*time.Minute); err != nil {
			return err
		}
		if *c.dep, err = create("/v1/deployments", map[string]any{
			"metadata": owned(c.name, "Environment", tw.env),
			"spec":     map[string]any{"component": c.comp},
		}); err != nil {
			return err
		}
		tw.deployments = append(tw.deployments, *c.dep)
	}
	for _, d := range tw.deployments {
		if err := a.waitTrueFor(ctx, "/v1/deployments/"+d, "Ready", 10*time.Minute); err != nil {
			return err
		}
	}
	return nil
}

// waitTrueFor waits for a condition to be True, polling every 2 s.
func (a api) waitTrueFor(ctx context.Context, path, typ string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		obj, err := a.do(ctx, http.MethodGet, path, nil)
		if err == nil && conditionTrue(obj, typ) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s: %s not True after %s", path, typ, timeout)
		}
		if err := sleep(ctx, 2*time.Second); err != nil {
			return err
		}
	}
}

// setEnv sets one variable on a Component, which makes a new release.
func (a api) setEnv(ctx context.Context, comp, key, value string) error {
	obj, err := a.do(ctx, http.MethodGet, "/v1/components/"+comp, nil)
	if err != nil {
		return err
	}
	spec, _ := obj["spec"].(map[string]any)
	if spec == nil {
		return fmt.Errorf("component %s has no spec", comp)
	}
	env, _ := spec["env"].(map[string]any)
	if env == nil {
		env = map[string]any{}
	}
	env[key] = value
	spec["env"] = env
	delete(obj, "status")
	_, err = a.do(ctx, http.MethodPut, "/v1/components/"+comp, obj)
	return err
}

// waitTraced waits until a pod of every Deployment's workload carries the
// trace endpoint variable, read from the cluster (the variable is in the
// release, not the Component's spec).
func (a api) waitTraced(ctx context.Context, cl *cluster, deps []string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		n, err := cl.podsWithEnv(ctx, "OTEL_EXPORTER_OTLP_ENDPOINT")
		if err == nil && n >= len(deps) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("traffic: %d of %d workloads carry the trace endpoint after %s (%v)", n, len(deps), timeout, err)
		}
		if err := sleep(ctx, 5*time.Second); err != nil {
			return err
		}
	}
}

// writeTrafficSummary renders the traffic phases, and the tracing
// package's cost as the traced phase over the untraced one.
func writeTrafficSummary(b *strings.Builder, t *TrafficRun) {
	if t == nil {
		return
	}
	fmt.Fprintf(b, "\n## HTTP traffic and tracing\n\nA caller calling a server every %d ms (%s), under the log load.\n\n", t.CallEveryMs, t.Image)
	b.WriteString("| Phase | Node mean / max (MiB) | Node CPU mean / max (mcores) | Networking (Cilium) MiB / mcores | Observability MiB / mcores | Tracing (collector) MiB / mcores | Workloads MiB / mcores | serendipd RSS / CPU |\n|---|---|---|---|---|---|---|---|\n")
	for _, ph := range []struct {
		name string
		l    *LoadRun
	}{{"HTTP traffic", t.Traffic}, {"HTTP traffic, traced", t.Tracing}} {
		if ph.l == nil {
			continue
		}
		l := ph.l
		g := func(name string) string {
			u, ok := l.Cluster.Groups[name]
			if !ok {
				return "–"
			}
			return fmt.Sprintf("%.0f / %.0f", u.MemoryMiB.Mean, u.CPUMillicores.Mean)
		}
		fmt.Fprintf(b, "| %s | %.0f / %.0f | %.0f / %.0f | %s | %s | %s | %s | %.1f MiB / %.2f mcores |\n", ph.name,
			l.Cluster.NodeMemoryMiB.Mean, l.Cluster.NodeMemoryMiB.Max, l.Cluster.NodeCPUMillicores.Mean, l.Cluster.NodeCPUMillicores.Max,
			g(GroupNetworking), g(GroupObservability), g(GroupTracing), g(GroupWorkloads), l.Serendipd.RSSMiB.Mean, l.Serendipd.CPUMillicores.Mean)
	}
	if t.Traffic != nil && t.Tracing != nil {
		fmt.Fprintf(b, "\n- Tracing installed in %s; traced − untraced, node: **%+.0f MiB mean (%+.0f at max), %+.0f mcores mean (%+.0f at max)**\n",
			fmtDur(t.TracingInstall),
			t.Tracing.Cluster.NodeMemoryMiB.Mean-t.Traffic.Cluster.NodeMemoryMiB.Mean, t.Tracing.Cluster.NodeMemoryMiB.Max-t.Traffic.Cluster.NodeMemoryMiB.Max,
			t.Tracing.Cluster.NodeCPUMillicores.Mean-t.Traffic.Cluster.NodeCPUMillicores.Mean, t.Tracing.Cluster.NodeCPUMillicores.Max-t.Traffic.Cluster.NodeCPUMillicores.Max)
		for _, ph := range []struct {
			name string
			l    *LoadRun
		}{{"untraced", t.Traffic}, {"traced", t.Tracing}} {
			if d := diskCost(ph.l, GroupObservability); d != nil {
				fmt.Fprintf(b, "- Observability volumes, %s: %+.1f MiB over %s, about %d MiB a day\n", ph.name, d.GrowthMiB, fmtDur(Duration(time.Duration(d.SpanSeconds*float64(time.Second)))), d.GrowthMiBPerDay)
			}
		}
		if t.TracesError != "" {
			fmt.Fprintf(b, "- Listing the server's traces failed: %s\n", t.TracesError)
		} else {
			fmt.Fprintf(b, "- The server's Deployment listed %d traces from the last 5 minutes at the end\n", t.Traces)
		}
	}
}
