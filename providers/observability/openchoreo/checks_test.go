package openchoreo

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/serendip-infra/serendip-infra/core/reconcile"
	"github.com/serendip-infra/serendip-infra/providers/kubernetes"
)

// After a restart the first resync waits out a settling fault once
// instead of running Helm straight away.
func TestInstall_RestartWaitsOutSettlingOnce(t *testing.T) {
	e := newTestEnv(t)
	e.installed(t)
	client := e.inst.client
	e.inst = newInstaller(e.helm.run, client) // a restarted serendipd
	e.inst.now = func() time.Time { return e.now }
	e.api.put(daemonSetPath, kubernetes.Object{Kind: "DaemonSet",
		Status: json.RawMessage(`{"desiredNumberScheduled":1,"numberReady":0}`)})

	state, err := e.inst.Install(context.Background(), instance(Fingerprint()))
	if err != nil || state.Ready || len(e.helm.calls) != 0 {
		t.Fatalf("right after a restart: %+v, %v, helm %v", state, err, e.helm.calls)
	}
	e.now = e.now.Add(settle)
	e.inst.Install(context.Background(), instance(Fingerprint()))
	if len(e.helm.calls) != 1 || e.helm.calls[0][2] != logsRelease {
		t.Errorf("after the window: helm %v, want the logs release", e.helm.calls)
	}
}

// No data from a query that answers is reported, never "repaired": no
// Helm and no applies, however long it lasts (RP-056).
func TestInstall_EmptyMetricsIsReportedNotRepaired(t *testing.T) {
	e := newTestEnv(t)
	e.installed(t)
	e.api.metrics = `{"memoryUsage":[null]}`
	for range 3 {
		e.now = e.now.Add(maxSettle)
		state, err := e.inst.Install(context.Background(), instance(Fingerprint()))
		if err != nil || state.Ready || !strings.Contains(state.Message, "no data for pod app-1") {
			t.Fatalf("Install() = %+v, %v", state, err)
		}
	}
	if len(e.helm.calls) != 0 || e.api.count("APPLY") != 0 {
		t.Errorf("helm %v, applies %d; want none", e.helm.calls, e.api.count("APPLY"))
	}
}

// Right after an install, no metrics yet is the first scrape still to
// come, and said so; after the settle window it's the mismatch warning.
func TestInstall_NoMetricsBeforeTheFirstScrape(t *testing.T) {
	e := newTestEnv(t)
	e.installed(t)
	e.api.metrics = `{"memoryUsage":[]}`
	state, err := e.inst.Install(context.Background(), instance(Fingerprint()))
	if err != nil || state.Ready || !strings.Contains(state.Message, "waiting for the first metrics") {
		t.Fatalf("just installed: %+v, %v", state, err)
	}
	e.now = e.now.Add(settle)
	state, err = e.inst.Install(context.Background(), instance(Fingerprint()))
	if err != nil || state.Ready || !strings.Contains(state.Message, "no data for pod") {
		t.Fatalf("after the settle window: %+v, %v", state, err)
	}
	if len(e.helm.calls) != 0 {
		t.Errorf("helm ran: %v", e.helm.calls)
	}
}

// An adapter answering with an error settles, then re-runs only the
// metrics release.
func TestInstall_MetricsErrorRepairsTheAdapter(t *testing.T) {
	e := newTestEnv(t)
	e.installed(t)
	inner := e.api.handler(t)
	e.inst.client = newTestKubeClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/proxy/") {
			http.Error(w, "no endpoints", http.StatusServiceUnavailable)
			return
		}
		inner(w, r)
	})
	e.now = e.now.Add(time.Minute)
	state, err := e.inst.Install(context.Background(), instance(Fingerprint()))
	if err != nil || state.Ready || state.Message != "the metrics query failed: status 503" || len(e.helm.calls) != 0 {
		t.Fatalf("inside the window: %+v, %v, helm %v", state, err, e.helm.calls)
	}
	e.now = e.now.Add(settle)
	e.inst.Install(context.Background(), instance(Fingerprint()))
	if len(e.helm.calls) != 1 || e.helm.calls[0][2] != metricsRelease {
		t.Errorf("helm = %v, want the metrics release", e.helm.calls)
	}
}

// A cancelled request is an error, not a package fault.
func TestInstall_CancelledContextIsAnError(t *testing.T) {
	e := newTestEnv(t)
	e.installed(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	state, err := e.inst.Install(ctx, instance(Fingerprint()))
	if err == nil || state.Message != "" {
		t.Errorf("Install() = %+v, %v; want an error", state, err)
	}
}

// Serendip's Fluent Bit config is the chart's render with the kubernetes
// filter on the API server, verified, and nothing else changed (RP-064).
func TestFluentBitConfig_OnlyTheMetadataLookupChanges(t *testing.T) {
	conf, err := fluentBitConf()
	if err != nil {
		t.Fatalf("fluentBitConf() = %v", err)
	}
	for _, want := range []string{"Use_Kubelet false\n", "tls.verify On\n",
		"Set openchoreo_cluster_instance " + fluentBitInstance + "\n", "HTTP_Port     2020\n", "Host openobserve\n"} {
		if !strings.Contains(conf, want) {
			t.Errorf("config lacks %q", want)
		}
	}
	if strings.Contains(conf, "Use_Kubelet true") || strings.Contains(conf, "tls.verify Off") {
		t.Errorf("config still has the kubelet lookup:\n%s", conf)
	}
	// Undoing the edits gives back the chart's render, byte for byte.
	undone := conf
	for _, e := range fluentBitEdits {
		undone = strings.Replace(undone, e.new, e.old, 1)
	}
	if undone != chartFluentBitConf {
		t.Errorf("the config differs from the chart's beyond fluentBitEdits")
	}
	var data map[string]string
	json.Unmarshal(fluentBitConfig(reconcile.FlowSource{}).Data, &data)
	if data["fluent-bit.conf"] != conf || data["parsers.conf"] != chartParsersConf || len(data) != 2 {
		t.Errorf("ConfigMap data keys = %v", slices.Collect(maps.Keys(data)))
	}
	if h := fluentBitConfigHash(reconcile.FlowSource{}); len(h) != 16 {
		t.Errorf("hash = %q", h)
	}
}

// A chart upgrade that moves an edited line fails loudly, not silently
// shipping the kubelet lookup.
func TestFluentBitConfig_MovedLineIsAnError(t *testing.T) {
	moved := strings.Replace(chartFluentBitConf, "    Use_Kubelet true\n", "", 1)
	if _, err := editFluentBitConf(moved); err == nil || !strings.Contains(err.Error(), "has 0 copies") {
		t.Errorf("editFluentBitConf(moved) err = %v", err)
	}
	twice := chartFluentBitConf + fluentBitEdits[0].old
	if _, err := editFluentBitConf(twice); err == nil || !strings.Contains(err.Error(), "has 2 copies") {
		t.Errorf("editFluentBitConf(twice) err = %v", err)
	}
}

func TestInstall_AdminSecretMissingKeysIsRefused(t *testing.T) {
	e := newTestEnv(t)
	broken := `{"ZO_ROOT_USER_EMAIL":"YQ=="}`
	e.api.put(adminPath, kubernetes.Object{Kind: "Secret", Data: json.RawMessage(broken)})
	_, err := e.inst.Install(context.Background(), instance(""))
	if err == nil || !strings.Contains(err.Error(), "lacks ZO_ROOT_USER_EMAIL or ZO_ROOT_USER_PASSWORD") {
		t.Fatalf("Install() err = %v", err)
	}
	if len(e.helm.calls) != 0 {
		t.Errorf("helm ran: %v", e.helm.calls)
	}
	if s, _ := e.api.get(adminPath); string(s.Data) != broken {
		t.Errorf("the Secret was changed: %s", s.Data)
	}
}

// The plane object must be gone before anything else is removed.
func TestUninstall_WaitsForThePlaneObject(t *testing.T) {
	e := newTestEnv(t)
	e.installed(t)
	e.api.keep[planeCRPath] = true // finalizing
	if err := e.inst.Uninstall(context.Background(), "pi-1"); !errors.Is(err, ErrPlaneRemaining) {
		t.Fatalf("Uninstall() = %v, want ErrPlaneRemaining", err)
	}
	if len(e.helm.calls) != 0 {
		t.Errorf("helm ran while the plane object remained: %v", e.helm.calls)
	}
	e.api.keep[planeCRPath] = false
	e.api.del(planeCRPath)
	if err := e.inst.Uninstall(context.Background(), "pi-1"); err != nil {
		t.Errorf("Uninstall() once gone = %v", err)
	}
}

// A failed step leaves the rest for the retry, which resumes cleanly.
func TestUninstall_ResumesAfterAHelmFailure(t *testing.T) {
	e := newTestEnv(t)
	e.installed(t)
	e.api.put(namespacePath, kubernetes.Object{Kind: "Namespace"})
	fail := true
	e.helm.respond = func(args []string) ([]byte, error) {
		if args[0] == "uninstall" && args[1] == logsRelease && fail {
			return []byte("timed out"), errors.New("exit status 1")
		}
		return nil, nil
	}
	if err := e.inst.Uninstall(context.Background(), "pi-1"); err == nil || !strings.Contains(err.Error(), "the logs store") {
		t.Fatalf("Uninstall() = %v, want the logs store's failure", err)
	}
	if _, ok := e.api.get(namespacePath); !ok {
		t.Error("the namespace went before every release did")
	}
	fail = false
	if err := e.inst.Uninstall(context.Background(), "pi-1"); err != nil {
		t.Fatalf("retry = %v", err)
	}
	if _, ok := e.api.get(namespacePath); ok {
		t.Error("namespace left after the retry")
	}
}

// The probe pod is read back by name; the cluster's Component pods are
// listed only when it's gone, and then another is picked.
func TestInstall_ProbePodIsReadBackNotRelisted(t *testing.T) {
	e := newTestEnv(t)
	e.installed(t) // the first check listed and picked app-1
	for range 5 {
		if state, err := e.inst.Install(context.Background(), instance(Fingerprint())); err != nil || !state.Ready {
			t.Fatalf("Install() = %+v, %v", state, err)
		}
	}
	if n := e.api.count("GET /api/v1/pods"); n != 0 {
		t.Errorf("cluster-wide pod lists = %d, want 0 while app-1 runs", n)
	}
	if n := e.api.count("GET " + appPodPath); n != 5 {
		t.Errorf("GETs of app-1 = %d, want one per resync", n)
	}

	e.api.del(appPodPath)
	other := componentPod("app-2", e.now.Add(-time.Hour))
	other.Metadata.Labels[labelComponent] = "c2-uid"
	e.api.put("/api/v1/namespaces/apps/pods/app-2", other)
	e.api.reset()
	if state, err := e.inst.Install(context.Background(), instance(Fingerprint())); err != nil || !state.Ready {
		t.Fatalf("Install() = %+v, %v", state, err)
	}
	if n := e.api.count("GET /api/v1/pods"); n != 1 {
		t.Errorf("cluster-wide pod lists = %d, want 1 once app-1 is gone", n)
	}
	if scope, _ := e.api.metricsBody["searchScope"].(map[string]any); scope["componentUid"] != "c2-uid" {
		t.Errorf("query scope = %v, want app-2's", scope)
	}
	if n := e.api.count("GET"); n != 7 {
		t.Errorf("GETs = %d, want 7 on the resync that re-picks (6 plus the missed pod)", n)
	}
}

func TestHasPoints(t *testing.T) {
	for _, tt := range []struct {
		in   string
		want bool
	}{
		{`[{"timestamp":"t","value":1}]`, true},
		{`[null, {"value":2}]`, true},
		{``, false}, {`null`, false}, {`[]`, false}, {`[null]`, false}, {`[{}]`, false},
		{`{}`, false}, {`0`, false}, {`""`, false},
	} {
		if got := hasPoints(json.RawMessage(tt.in)); got != tt.want {
			t.Errorf("hasPoints(%s) = %v", tt.in, got)
		}
	}
}
