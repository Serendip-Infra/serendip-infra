package cilium

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/serendip-infra/serendip-infra/core/reconcile"
	"github.com/serendip-infra/serendip-infra/providers/kubernetes"
)

// fakeClient answers GetRaw from fixed bodies and keeps the
// CiliumNetworkPolicies it's given, so the managed list reflects writes.
type fakeClient struct {
	mu       sync.Mutex
	bodies   map[string]any // path -> JSON string or error
	cnps     map[string]kubernetes.Object
	applies  []string
	deletes  []string
	applyErr error
	// goneOnDelete answers every delete as already gone.
	goneOnDelete bool
	// guard is the node guard as the server has it; guardApplies counts
	// its writes (kept apart from the L7 policies').
	guard        *kubernetes.Object
	guardApplies int
}

func newFake(bodies map[string]any) *fakeClient {
	return &fakeClient{bodies: bodies, cnps: map[string]kubernetes.Object{}}
}

func (f *fakeClient) GetRaw(_ context.Context, path string) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if path == guardPath() {
		if err, ok := f.bodies[path].(error); ok {
			return nil, err
		}
		if f.guard == nil {
			return nil, kubernetes.ErrNotFound
		}
		return json.Marshal(f.guard)
	}
	if strings.HasPrefix(path, "/apis/cilium.io/v2/ciliumnetworkpolicies?") {
		if err, ok := f.bodies["cnps"].(error); ok {
			return nil, err
		}
		items := []kubernetes.Object{}
		for _, o := range f.cnps {
			items = append(items, o)
		}
		b, _ := json.Marshal(map[string]any{"items": items})
		return b, nil
	}
	switch v := f.bodies[path].(type) {
	case string:
		return json.RawMessage(v), nil
	case error:
		return nil, v
	}
	return nil, kubernetes.ErrNotFound
}

func (f *fakeClient) Apply(_ context.Context, path string, obj kubernetes.Object, manager string) (kubernetes.Object, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.applyErr != nil {
		return kubernetes.Object{}, f.applyErr
	}
	if manager != kubernetes.FieldManager {
		return kubernetes.Object{}, errors.New("wrong field manager " + manager)
	}
	if path == guardPath() && obj.Metadata.Name == guardName && obj.Metadata.Namespace == "" {
		f.guardApplies++
		obj.Metadata.UID = "server-uid"
		f.guard = &obj
		return obj, nil
	}
	if path != cnpPath(obj.Metadata.Namespace, obj.Metadata.Name) {
		return kubernetes.Object{}, errors.New("apply path " + path + " doesn't match the object")
	}
	f.applies = append(f.applies, path)
	// The server adds fields the provider doesn't set.
	obj.Metadata.UID = "server-uid"
	obj.Metadata.ResourceVersion = "1"
	f.cnps[path] = obj
	return obj, nil
}

func (f *fakeClient) Delete(_ context.Context, path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if path == guardPath() {
		f.guard = nil
		return nil
	}
	f.deletes = append(f.deletes, path)
	if f.goneOnDelete {
		delete(f.cnps, path)
		return kubernetes.ErrNotFound
	}
	if _, ok := f.cnps[path]; !ok {
		return kubernetes.ErrNotFound
	}
	delete(f.cnps, path)
	return nil
}

// pinnedConfig is cilium-config as the chart renders values.yaml (live on
// t025, Cilium 1.20.5, trimmed to the keys read).
func pinnedConfig() map[string]string {
	return map[string]string{
		"enable-hubble":                      "true",
		"enable-policy":                      "default",
		"enable-k8s-networkpolicy":           "true",
		"enable-l7-proxy":                    "true",
		"hubble-metrics":                     pinnedHTTPMetric + " drop flow",
		"hubble-export-file-path":            pinnedExportPath,
		"hubble-export-fieldmask":            pinnedFieldMask,
		"hubble-export-fieldaggregate":       pinnedFieldAgg,
		"hubble-export-aggregation-interval": "60s",
		"hubble-export-allowlist":            pinnedAllowList,
		"hubble-export-denylist":             pinnedDenyList,
		"http-retry-count":                   "0",
		"http-normalize-path":                "false",
		"ipam":                               "kubernetes",
		"kube-proxy-replacement":             "false",
	}
}

func configBody(cfg map[string]string) string {
	b, _ := json.Marshal(map[string]any{"data": cfg})
	return string(b)
}

func dsBody(image string, desired, ready int) string {
	return `{"spec":{"template":{"spec":{"containers":[{"name":"cilium-agent","image":"` + image + `"}]}}},
 "status":{"desiredNumberScheduled":` + itoa(desired) + `,"numberReady":` + itoa(ready) + `}}`
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

const liveImage = "quay.io/cilium/cilium:v1.20.5@sha256:aa72499f075ad729947ea91b026218aceba24f1a8e4534bf36b8368c7c561526"

const kindNodes = `{"items":[{"spec":{"podCIDR":"10.244.0.0/24","podCIDRs":["10.244.0.0/24"]}}]}`

func baseBodies(cfg map[string]string) map[string]any {
	return map[string]any{
		configPath:    configBody(cfg),
		daemonSetPath: dsBody(liveImage, 1, 1),
		nodesPath:     kindNodes,
	}
}

func TestFacts(t *testing.T) {
	kind := []netip.Prefix{netip.MustParsePrefix("10.244.0.0/24")}
	with := func(edit func(map[string]string)) map[string]string {
		c := pinnedConfig()
		edit(c)
		return c
	}
	tests := []struct {
		name    string
		bodies  map[string]any
		want    reconcile.NetworkFacts
		unavail []string // substrings, each in some Unavailable entry
		wantErr bool
	}{
		{
			name:   "pinned values, as live on t025",
			bodies: baseBodies(pinnedConfig()),
			want: reconcile.NetworkFacts{Plugin: "cilium", Version: "1.20.5", PolicyEnforcement: reconcile.PolicyEnforced,
				PolicyReason: "Cilium's own policy enforcement", Flows: true, RequestMetrics: true, PodRanges: kind},
		},
		{
			name: "Cilium's default retries hide failures: no request metrics",
			bodies: baseBodies(with(func(c map[string]string) {
				delete(c, "http-retry-count")
			})),
			want: reconcile.NetworkFacts{Plugin: "cilium", Version: "1.20.5", PolicyEnforcement: reconcile.PolicyEnforced,
				PolicyReason: "Cilium's own policy enforcement", Flows: true, PodRanges: kind},
			unavail: []string{"retries failed requests"},
		},
		{
			name: "path normalization on",
			bodies: baseBodies(with(func(c map[string]string) {
				c["http-normalize-path"] = "true"
			})),
			want: reconcile.NetworkFacts{Plugin: "cilium", Version: "1.20.5", PolicyEnforcement: reconcile.PolicyEnforced,
				PolicyReason: "Cilium's own policy enforcement", Flows: true, PodRanges: kind},
			unavail: []string{"rewrites request paths"},
		},
		{
			name: "Hubble off",
			bodies: baseBodies(with(func(c map[string]string) {
				c["enable-hubble"] = "false"
			})),
			want: reconcile.NetworkFacts{Plugin: "cilium", Version: "1.20.5", PolicyEnforcement: reconcile.PolicyEnforced,
				PolicyReason: "Cilium's own policy enforcement", PodRanges: kind},
			unavail: []string{"flows: Hubble is turned off", "requests: Hubble is turned off"},
		},
		{
			name: "no httpV2 metric with pod labels",
			bodies: baseBodies(with(func(c map[string]string) {
				c["hubble-metrics"] = "httpV2:sourceContext=pod;destinationContext=pod drop flow"
			})),
			want: reconcile.NetworkFacts{Plugin: "cilium", Version: "1.20.5", PolicyEnforcement: reconcile.PolicyEnforced,
				PolicyReason: "Cilium's own policy enforcement", Flows: true, PodRanges: kind},
			unavail: []string{"requests: Hubble doesn't make the HTTP metrics"},
		},
		{
			name: "export off",
			bodies: baseBodies(with(func(c map[string]string) {
				delete(c, "hubble-export-file-path")
			})),
			want: reconcile.NetworkFacts{Plugin: "cilium", Version: "1.20.5", PolicyEnforcement: reconcile.PolicyEnforced,
				PolicyReason: "Cilium's own policy enforcement", RequestMetrics: true, PodRanges: kind},
			unavail: []string{"flows: Hubble's flow export is turned off"},
		},
		{
			name: "another field mask",
			bodies: baseBodies(with(func(c map[string]string) {
				c["hubble-export-fieldmask"] = "time verdict source destination"
			})),
			want: reconcile.NetworkFacts{Plugin: "cilium", Version: "1.20.5", PolicyEnforcement: reconcile.PolicyEnforced,
				PolicyReason: "Cilium's own policy enforcement", RequestMetrics: true, PodRanges: kind},
			unavail: []string{"other fields than Serendip reads"},
		},
		{
			name: "other filters: still readable, said",
			bodies: baseBodies(with(func(c map[string]string) {
				c["hubble-export-denylist"] = ""
			})),
			want: reconcile.NetworkFacts{Plugin: "cilium", Version: "1.20.5", PolicyEnforcement: reconcile.PolicyEnforced,
				PolicyReason: "Cilium's own policy enforcement", Flows: true, RequestMetrics: true, PodRanges: kind},
			unavail: []string{"export filters differ"},
		},
		{
			name: "older than 1.20: flows not read",
			bodies: func() map[string]any {
				b := baseBodies(pinnedConfig())
				b[daemonSetPath] = dsBody("quay.io/cilium/cilium:v1.18.4", 1, 1)
				return b
			}(),
			want: reconcile.NetworkFacts{Plugin: "cilium", Version: "1.18.4", PolicyEnforcement: reconcile.PolicyEnforced,
				PolicyReason: "Cilium's own policy enforcement", RequestMetrics: true, PodRanges: kind},
			unavail: []string{"from Cilium 1.20 on"},
		},
		{
			name: "policy never",
			bodies: baseBodies(with(func(c map[string]string) {
				c["enable-policy"] = "never"
			})),
			want: reconcile.NetworkFacts{Plugin: "cilium", Version: "1.20.5", PolicyEnforcement: reconcile.PolicyNotEnforced,
				PolicyReason: "Cilium was configured never to enforce network policies", Flows: true, RequestMetrics: true, PodRanges: kind},
		},
		{
			name: "Kubernetes policies ignored",
			bodies: baseBodies(with(func(c map[string]string) {
				c["enable-k8s-networkpolicy"] = "false"
			})),
			want: reconcile.NetworkFacts{Plugin: "cilium", Version: "1.20.5", PolicyEnforcement: reconcile.PolicyNotEnforced,
				PolicyReason: "Cilium was configured to ignore Kubernetes network policies", Flows: true, RequestMetrics: true, PodRanges: kind},
		},
		{
			name: "cluster-pool IPAM reads CiliumNodes",
			bodies: func() map[string]any {
				b := baseBodies(with(func(c map[string]string) { c["ipam"] = "cluster-pool" }))
				delete(b, nodesPath)
				b[ciliumNodes] = `{"items":[{"spec":{"ipam":{"podCIDRs":["10.0.1.0/24"]}}},
				 {"spec":{"ipam":{"podCIDRs":["10.0.0.0/24","bogus"],"pools":{"allocated":[{"pool":"p","cidrs":["10.9.0.0/26"]}]}}}}]}`
				return b
			}(),
			want: reconcile.NetworkFacts{Plugin: "cilium", Version: "1.20.5", PolicyEnforcement: reconcile.PolicyEnforced,
				PolicyReason: "Cilium's own policy enforcement", Flows: true, RequestMetrics: true,
				PodRanges: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/24"), netip.MustParsePrefix("10.0.1.0/24"), netip.MustParsePrefix("10.9.0.0/26")}},
			unavail: []string{"a node's range doesn't parse"},
		},
		{
			name: "agent not ready everywhere",
			bodies: func() map[string]any {
				b := baseBodies(pinnedConfig())
				b[daemonSetPath] = dsBody(liveImage, 3, 2)
				return b
			}(),
			want: reconcile.NetworkFacts{Plugin: "cilium", Version: "1.20.5", PolicyEnforcement: reconcile.PolicyEnforced,
				PolicyReason: "Cilium's own policy enforcement", Flows: true, RequestMetrics: true, PodRanges: kind},
			unavail: []string{"ready on 2 of 3 nodes"},
		},
		{
			name: "config unreadable: unknown, with what was read",
			bodies: func() map[string]any {
				b := baseBodies(nil)
				b[configPath] = errors.New("forbidden")
				return b
			}(),
			want: reconcile.NetworkFacts{Plugin: "cilium", Version: "1.20.5", PolicyEnforcement: reconcile.PolicyUnknown,
				PolicyReason: "Cilium's configuration can't be read", PodRanges: kind},
			unavail: []string{"flows: Cilium's configuration can't be read", "requests: Cilium's configuration can't be read"},
			wantErr: true,
		},
	}
	at := time.Date(2026, 11, 20, 3, 0, 0, 0, time.UTC)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := New(newFake(tt.bodies))
			p.now = func() time.Time { return at }
			p.remember(Report{}, nil) // one clean visibility pass
			got, err := p.Facts(context.Background())
			if (err != nil) != tt.wantErr {
				t.Fatalf("Facts() error = %v, wantErr %v", err, tt.wantErr)
			}
			for _, s := range tt.unavail {
				if !slices.ContainsFunc(got.Unavailable, func(u string) bool { return strings.Contains(u, s) }) {
					t.Errorf("Unavailable = %q, want an entry with %q", got.Unavailable, s)
				}
			}
			if tt.unavail == nil && got.Unavailable != nil {
				t.Errorf("Unavailable = %q, want none", got.Unavailable)
			}
			got.Unavailable = nil
			tt.want.ReadAt = at
			gj, _ := json.Marshal(got)
			wj, _ := json.Marshal(tt.want)
			if !bytes.Equal(gj, wj) {
				t.Errorf("Facts() =\n %s\nwant\n %s", gj, wj)
			}
		})
	}
}

func TestFlowSource(t *testing.T) {
	p := New(newFake(baseBodies(pinnedConfig())))
	got, err := p.FlowSource(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := reconcile.FlowSource{HostPath: "/var/run/cilium/hubble", File: "events.log", Format: FlowFormat}
	if got != want {
		t.Errorf("FlowSource() = %+v, want %+v", got, want)
	}

	cfg := pinnedConfig()
	delete(cfg, "hubble-export-file-path")
	p = New(newFake(baseBodies(cfg)))
	if got, err := p.FlowSource(context.Background()); err != nil || !got.IsZero() {
		t.Errorf("FlowSource() with the export off = %+v, %v; want zero", got, err)
	}
}

// TestPinnedMatchValues keeps the settings Facts checks in step with
// values.yaml, which the chart renders into cilium-config.
func TestPinnedMatchValues(t *testing.T) {
	v := string(Values())
	join := func(s string) string { return "[" + strings.ReplaceAll(s, " ", ", ") + "]" }
	for _, want := range []string{
		`- "` + pinnedHTTPMetric + `"`,
		"filePath: " + pinnedExportPath,
		"fieldMask: " + join(pinnedFieldMask),
		"fieldAggregate: " + join(pinnedFieldAgg),
		`aggregationInterval: "` + pinnedInterval + `"`,
		`http-retry-count: "` + pinnedRetryCount + `"`,
		`http-normalize-path: "` + pinnedNormalizePath + `"`,
		"mode: kubernetes",
		"kubeProxyReplacement: false",
	} {
		if !strings.Contains(v, want) {
			t.Errorf("values.yaml lacks %q", want)
		}
	}
	for _, list := range []struct{ key, pinned string }{{"allowList:", pinnedAllowList}, {"denyList:", pinnedDenyList}} {
		for _, entry := range strings.Split(list.pinned, "} {") {
			entry = "'" + strings.TrimSuffix(strings.TrimPrefix(entry, "{"), "}") + "'"
			entry = "'{" + strings.Trim(entry, "'") + "}'"
			if !strings.Contains(v, "- "+entry) {
				t.Errorf("values.yaml %s lacks %s", list.key, entry)
			}
		}
	}
	// T001's final values are the ones embedded.
	t001, err := os.ReadFile("testdata/t001-values.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(v, string(t001)) {
		t.Error("values.yaml differs from testdata/t001-values.yaml")
	}
}

func TestDetect(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("boom")
	for _, tc := range []struct {
		name    string
		bodies  map[string]any
		want    bool
		wantErr bool
	}{
		{"cilium", map[string]any{daemonSetPath: `{}`}, true, false},
		{"not cilium", map[string]any{}, false, false},
		{"can't tell", map[string]any{daemonSetPath: boom}, false, true},
	} {
		got, err := Detect(ctx, newFake(tc.bodies))
		if got != tc.want || (err != nil) != tc.wantErr {
			t.Errorf("%s: Detect = %v, %v; want %v, error %v", tc.name, got, err, tc.want, tc.wantErr)
		}
	}
}
