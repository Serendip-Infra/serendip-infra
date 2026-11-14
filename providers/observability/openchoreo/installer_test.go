package openchoreo

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/serendip-infra/serendip-infra/core/reconcile"
	"github.com/serendip-infra/serendip-infra/core/resource"
	"github.com/serendip-infra/serendip-infra/providers/kubernetes"
)

// fakeAPI is a minimal in-memory Kubernetes API keyed by item path. An
// apply stores the object (keeping any status already there), a POST
// creates one (409 if it exists), a DELETE removes it, and a GET of a
// collection lists the objects directly under it. A POST under a Service
// proxy path answers with metrics. calls records "METHOD path" (APPLY for
// server-side apply), and the fake Helm adds "HELM <verb> <release>" to
// the same timeline.
type fakeAPI struct {
	mu      sync.Mutex
	objects map[string]kubernetes.Object
	calls   []string
	// metrics answers the metrics query; metricsBody is the last request.
	metrics     string
	metricsBody map[string]any
	// keep lists paths a DELETE leaves in place (a terminating namespace).
	keep map[string]bool
}

const healthyMetrics = `{"cpuUsage":[{"timestamp":"t","value":1}],"memoryUsage":[{"timestamp":"t","value":20}]}`

func newFakeAPI() *fakeAPI {
	return &fakeAPI{objects: map[string]kubernetes.Object{}, metrics: healthyMetrics, keep: map[string]bool{}}
}

func (f *fakeAPI) put(path string, obj kubernetes.Object) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objects[path] = obj
}

func (f *fakeAPI) del(path string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.objects, path)
}

func (f *fakeAPI) get(path string) (kubernetes.Object, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	o, ok := f.objects[path]
	return o, ok
}

func (f *fakeAPI) log(call string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
}

func (f *fakeAPI) timeline() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func (f *fakeAPI) count(prefix string) int {
	n := 0
	for _, c := range f.timeline() {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func (f *fakeAPI) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = nil
}

// lists are the collections that answer an empty list rather than 404.
var lists = []string{planePodsPath, networkPoliciesPath, "/api/v1/pods"}

func (f *fakeAPI) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		method, path := r.Method, r.URL.Path
		if method == http.MethodPatch && r.Header.Get("Content-Type") == "application/apply-patch+yaml" {
			method = "APPLY"
		}
		f.calls = append(f.calls, method+" "+path)
		switch method {
		case "APPLY":
			var in kubernetes.Object
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Errorf("decode apply %s: %v", path, err)
			}
			if old, ok := f.objects[path]; ok {
				in.Status = old.Status
			}
			f.objects[path] = in
			json.NewEncoder(w).Encode(in)
		case http.MethodGet:
			if obj, ok := f.objects[path]; ok {
				json.NewEncoder(w).Encode(obj)
				return
			}
			items := []kubernetes.Object{}
			listed := slices.Contains(lists, path)
			for p, obj := range f.objects {
				if path == "/api/v1/pods" && isNamespacedPod(p) {
					items = append(items, obj) // every namespace's pods
					continue
				}
				if rest, ok := strings.CutPrefix(p, path+"/"); ok && !strings.Contains(rest, "/") {
					items = append(items, obj)
					listed = true
				}
			}
			if !listed {
				http.NotFound(w, r)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"items": items})
		case http.MethodDelete:
			if _, ok := f.objects[path]; !ok {
				http.NotFound(w, r)
				return
			}
			if !f.keep[path] {
				delete(f.objects, path)
			}
			w.WriteHeader(http.StatusOK)
		case http.MethodPost:
			if strings.Contains(path, "/proxy/") {
				f.metricsBody = nil
				json.NewDecoder(r.Body).Decode(&f.metricsBody)
				w.Write([]byte(f.metrics))
				return
			}
			var in kubernetes.Object
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Errorf("decode create %s: %v", path, err)
			}
			item := path + "/" + in.Metadata.Name
			if _, ok := f.objects[item]; ok {
				w.WriteHeader(http.StatusConflict)
				return
			}
			f.objects[item] = in
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(in)
		default:
			t.Errorf("unexpected %s %s", r.Method, path)
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}
}

// isNamespacedPod reports whether p is /api/v1/namespaces/<ns>/pods/<name>.
func isNamespacedPod(p string) bool {
	parts := strings.Split(strings.TrimPrefix(p, "/api/v1/namespaces/"), "/")
	return strings.HasPrefix(p, "/api/v1/namespaces/") && len(parts) == 3 && parts[1] == "pods"
}

func newTestKubeClient(t *testing.T, handler http.HandlerFunc) *kubernetes.Client {
	t.Helper()
	srv := httptest.NewTLSServer(handler)
	t.Cleanup(srv.Close)
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	client, err := kubernetes.NewClient(kubernetes.Config{Host: srv.URL, CAData: caPEM})
	if err != nil {
		t.Fatalf("NewClient() = %v", err)
	}
	return client
}

// fakeHelm records each invocation on the API's timeline. respond, when
// set, answers a call; otherwise an upgrade "installs" the release's
// ready pods (and Fluent Bit) and anything else succeeds.
type fakeHelm struct {
	api     *fakeAPI
	calls   [][]string
	respond func(args []string) ([]byte, error)
	// values is the Prometheus values file's content, read during its run.
	values []byte
}

func (f *fakeHelm) run(_ context.Context, args ...string) ([]byte, error) {
	f.calls = append(f.calls, args)
	f.api.log("HELM " + args[0] + " " + releaseArg(args))
	if k := slices.Index(args, "-f"); k >= 0 {
		f.values, _ = os.ReadFile(args[k+1])
	}
	if f.respond != nil {
		if out, err := f.respond(args); out != nil || err != nil {
			return out, err
		}
	}
	if args[0] == "upgrade" {
		installRelease(f.api, args[2])
	}
	return nil, nil
}

func releaseArg(args []string) string {
	switch args[0] {
	case "upgrade":
		return args[2]
	case "status", "uninstall", "rollback":
		return args[1]
	}
	return ""
}

// installRelease puts what a release's chart runs: a ready pod per
// workload, Fluent Bit's DaemonSet, the chart's policy.
func installRelease(api *fakeAPI, name string) {
	for k, w := range workloads {
		if w.release == name {
			api.put(fmt.Sprintf("%s/pod-%d", planePodsPath, k), readyPod(w.selector))
		}
	}
	switch name {
	case planeRelease:
		api.put(networkPoliciesPath+"/"+chartPolicyName, kubernetes.Object{Kind: "NetworkPolicy",
			Metadata: kubernetes.ObjectMeta{Name: chartPolicyName}})
	case logsRelease:
		api.put(fluentBitPodPath, kubernetes.Object{Kind: "Pod",
			Metadata: kubernetes.ObjectMeta{Name: "fluent-bit-1", Labels: fluentBitSelector}})
		api.put(daemonSetPath, kubernetes.Object{Kind: "DaemonSet",
			Status: json.RawMessage(`{"desiredNumberScheduled":1,"numberReady":1}`)})
	}
}

func readyPod(labels map[string]string) kubernetes.Object {
	return kubernetes.Object{Kind: "Pod", Metadata: kubernetes.ObjectMeta{Labels: labels},
		Status: json.RawMessage(`{"conditions":[{"type":"Ready","status":"True"}]}`)}
}

const (
	caSecretPath  = "/api/v1/namespaces/openchoreo-control-plane/secrets/cluster-gateway-ca"
	caConfigPath  = "/api/v1/namespaces/openchoreo-observability-plane/configmaps/cluster-gateway-ca"
	adminPath     = secretsPath + "/" + adminSecretName
	observerPath  = secretsPath + "/" + observerSecretName
	fluentBitPath = configMapsPath + "/" + fluentBitConfigName
	// fluentBitPodPath is the fake logs release's Fluent Bit pod.
	fluentBitPodPath = planePodsPath + "/fluent-bit-1"
	chartPolicy      = networkPoliciesPath + "/" + chartPolicyName
	planeCRPath      = observabilityPlanesPath + "/" + PlaneName
	namespacePath    = "/api/v1/namespaces/" + PlaneNamespace
	appPodPath       = "/api/v1/namespaces/apps/pods/app-1"
	metricsPath      = "/api/v1/namespaces/openchoreo-observability-plane/services/metrics-adapter:9099/proxy/api/v1/metrics/query"
)

type testEnv struct {
	api  *fakeAPI
	helm *fakeHelm
	inst *Installer
	now  time.Time
}

// newTestEnv is an installer over a fake API holding the control plane's
// CA and a Component pod that has run for an hour.
func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	e := &testEnv{api: newFakeAPI(), now: time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)}
	e.api.put(caSecretPath, kubernetes.Object{APIVersion: "v1", Kind: "Secret",
		Data: json.RawMessage(`{"ca.crt":"LS0tLUNBLS0tLQ=="}`)}) // "----CA----"
	e.api.put(appPodPath, componentPod("app-1", e.now.Add(-time.Hour)))
	e.helm = &fakeHelm{api: e.api}
	e.inst = newInstaller(e.helm.run, newTestKubeClient(t, e.api.handler(t)))
	e.inst.now = func() time.Time { return e.now }
	return e
}

func componentPod(name string, started time.Time) kubernetes.Object {
	return kubernetes.Object{Kind: "Pod",
		Metadata: kubernetes.ObjectMeta{Name: name, Namespace: "apps", Labels: map[string]string{
			labelNamespace: "default", labelProject: "p-uid", labelComponent: "c-uid", labelEnvironment: "e-uid",
		}},
		Status: json.RawMessage(`{"startTime":"` + started.Format(time.RFC3339) +
			`","conditions":[{"type":"Ready","status":"True"}]}`),
	}
}

// installed runs a first install and clears the timeline.
func (e *testEnv) installed(t *testing.T) {
	t.Helper()
	state, err := e.inst.Install(context.Background(), instance(""))
	if err != nil || !state.Ready {
		t.Fatalf("first Install() = %+v, %v", state, err)
	}
	e.helm.calls = nil
	e.api.reset()
}

func instance(applied string) resource.ProviderInstance {
	var inst resource.ProviderInstance
	inst.Metadata.ID = "pi-1"
	inst.Status.Applied = applied
	return inst
}

func TestInstall_FirstInstallAppliesEverythingInOrder(t *testing.T) {
	e := newTestEnv(t)
	state, err := e.inst.Install(context.Background(), instance(""))
	if err != nil {
		t.Fatalf("Install() = %v", err)
	}
	if !state.Ready || state.Applied != Fingerprint() || state.Message != readyMessage {
		t.Errorf("state = %+v", state)
	}

	var order []string
	for _, c := range e.api.timeline() {
		if strings.HasPrefix(c, "HELM ") || strings.HasPrefix(c, "APPLY ") || strings.HasPrefix(c, "POST ") {
			order = append(order, c)
		}
	}
	want := []string{
		"APPLY " + namespacePath,
		"APPLY " + caConfigPath,
		"APPLY " + observerPath,
		"POST " + secretsPath,
		"HELM upgrade " + planeRelease,
		"APPLY " + fluentBitPath, // before the logs module, whose DaemonSet mounts it
		"HELM upgrade " + logsRelease,
		"HELM upgrade " + prometheusRelease,
		"HELM upgrade " + metricsRelease,
		"APPLY " + planeCRPath,
		"POST " + metricsPath, // the readiness query
	}
	if !slices.Equal(order, want) {
		t.Errorf("order =\n  %s\nwant\n  %s", strings.Join(order, "\n  "), strings.Join(want, "\n  "))
	}

	var cm map[string]string
	ca, _ := e.api.get(caConfigPath)
	json.Unmarshal(ca.Data, &cm)
	if cm["ca.crt"] != "----CA----" {
		t.Errorf("CA copy = %v", cm)
	}
	cr, _ := e.api.get(planeCRPath)
	if s := string(cr.Spec); !strings.Contains(s, `"observerURL":"http://observer.openchoreo-observability-plane:8080"`) ||
		!strings.Contains(s, `"name":"cluster-agent-tls","namespace":"openchoreo-observability-plane"`) {
		t.Errorf("ObservabilityPlane spec = %s", s)
	}
	fb, _ := e.api.get(fluentBitPath)
	var fbData map[string]string
	json.Unmarshal(fb.Data, &fbData)
	if !strings.Contains(fbData["fluent-bit.conf"], "Use_Kubelet false") || fb.Metadata.Labels["app.kubernetes.io/managed-by"] != "serendip" {
		t.Errorf("Fluent Bit config = %+v", fb)
	}
}

// wantRestarted checks the repair deleted Fluent Bit's pod, after the
// config was re-applied, and no other pod.
func wantRestarted(t *testing.T, e *testEnv) {
	t.Helper()
	tl := e.api.timeline()
	apply, del := slices.Index(tl, "APPLY "+fluentBitPath), slices.Index(tl, "DELETE "+fluentBitPodPath)
	if apply < 0 || del < apply {
		t.Errorf("Fluent Bit not restarted after its config was re-applied: %v", tl)
	}
	if n := e.api.count("DELETE "); n != 1 {
		t.Errorf("deletes = %d, want only Fluent Bit's pod", n)
	}
}

func TestInstall_HelmArguments(t *testing.T) {
	e := newTestEnv(t)
	e.installed(t)
	e.inst.Install(context.Background(), instance("")) // a second full install, recorded
	byRelease := map[string][]string{}
	for _, c := range e.helm.calls {
		byRelease[c[2]] = c
	}
	tests := []struct {
		release string
		want    []string
	}{
		{planeRelease, []string{"--version 1.3.1", "--set security.enabled=false", "--set gateway.enabled=false",
			"--set observer.secretName=observer-secret", "--set observer.extraEnvs[0].value=http://observer.openchoreo-observability-plane:8080"}},
		{logsRelease, []string{"--version 0.8.0", "--set-string openobserve-standalone.config.ZO_TELEMETRY=false",
			"--set-string openobserve-standalone.config.ZO_COMPACT_DATA_RETENTION_DAYS=3",
			"--set openobserve-standalone.persistence.size=5Gi",
			// RP-064: off the host network, on Serendip's config, rolled
			// when it changes.
			"--set fluent-bit.hostNetwork=false", "--set fluent-bit.dnsPolicy=ClusterFirst",
			"--set fluent-bit.existingConfigMap=serendip-fluent-bit",
			"--set-string fluent-bit.podAnnotations.serendip-fluent-bit-config=" + fluentBitConfigHash()}},
		{prometheusRelease, []string{"--version 29.47.2", "oci://ghcr.io/prometheus-community/charts/prometheus"}},
		{metricsRelease, []string{"--version 0.9.3", "--set kube-prometheus-stack.enabled=false"}},
	}
	for _, tt := range tests {
		args := strings.Join(byRelease[tt.release], " ")
		for _, w := range tt.want {
			if !strings.Contains(args, w) {
				t.Errorf("%s: args %q lack %q", tt.release, args, w)
			}
		}
		if !strings.Contains(args, "--namespace openchoreo-observability-plane --create-namespace") ||
			!strings.HasSuffix(args, "--wait --timeout 15m") {
			t.Errorf("%s: args = %q", tt.release, args)
		}
	}
	// The values file holds the embedded values while Helm runs, and is
	// gone afterwards; the placeholder never reaches Helm.
	if !bytes.Equal(e.helm.values, prometheusValues) {
		t.Errorf("values file = %q", e.helm.values)
	}
	prom := byRelease[prometheusRelease]
	path := prom[slices.Index(prom, "-f")+1]
	if path == valuesFileArg {
		t.Errorf("the placeholder reached helm")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("values file %s left behind: %v", path, err)
	}
}

func TestInstall_AdminCredentialsGeneratedOnceAndKept(t *testing.T) {
	e := newTestEnv(t)
	e.installed(t)
	first, ok := e.api.get(adminPath)
	if !ok {
		t.Fatal("the admin Secret wasn't created")
	}
	var data map[string][]byte
	json.Unmarshal(first.Data, &data)
	pw := string(data["ZO_ROOT_USER_PASSWORD"])
	if string(data["ZO_ROOT_USER_EMAIL"]) != adminEmail || len(pw) != passwordLength || !strongEnough(pw) {
		t.Errorf("credentials = %q / %q", data["ZO_ROOT_USER_EMAIL"], pw)
	}
	// A full re-install doesn't touch it.
	e.inst.Install(context.Background(), instance(""))
	if n := e.api.count("POST " + secretsPath); n != 0 {
		t.Errorf("creates on re-install = %d, want 0", n)
	}
	again, _ := e.api.get(adminPath)
	if string(again.Data) != string(first.Data) {
		t.Errorf("the admin Secret changed")
	}
}

func TestInstall_UnchangedResyncIsCheap(t *testing.T) {
	e := newTestEnv(t)
	e.installed(t)
	const resyncs = 10
	for range resyncs {
		state, err := e.inst.Install(context.Background(), instance(Fingerprint()))
		if err != nil || !state.Ready || state.Message != readyMessage {
			t.Fatalf("Install() = %+v, %v", state, err)
		}
	}
	if len(e.helm.calls) != 0 {
		t.Errorf("helm calls = %v, want none", e.helm.calls)
	}
	if n := e.api.count("APPLY"); n != 0 {
		t.Errorf("applies = %d, want none", n)
	}
	// The task's bound is 8; the checks make 6.
	if n := e.api.count("GET"); n != 6*resyncs {
		t.Errorf("GETs = %d, want 6 per resync", n)
	}
	if n := e.api.count("POST"); n != resyncs {
		t.Errorf("POSTs = %d, want one metrics query per resync", n)
	}
}

// A fault is repaired straight away unless it may only be settling; Helm
// runs only for the release at fault, and none for hand-built objects.
func TestInstall_Repairs(t *testing.T) {
	tests := []struct {
		name     string
		break_   func(e *testEnv)
		wantHelm []string // releases
		check    func(t *testing.T, e *testEnv)
	}{
		{"deleted Fluent Bit config", func(e *testEnv) { e.api.del(fluentBitPath) }, nil,
			func(t *testing.T, e *testEnv) {
				if fb, ok := e.api.get(fluentBitPath); !ok || !dataEqual(fb.Data, fluentBitConfig().Data) {
					t.Error("Fluent Bit config not restored")
				}
				wantRestarted(t, e)
			}},
		{"edited Fluent Bit config", func(e *testEnv) {
			fb, _ := e.api.get(fluentBitPath)
			fb.Data = mustJSON(map[string]string{"fluent-bit.conf": chartFluentBitConf, "parsers.conf": chartParsersConf})
			e.api.put(fluentBitPath, fb)
		}, nil,
			func(t *testing.T, e *testEnv) {
				if fb, _ := e.api.get(fluentBitPath); !dataEqual(fb.Data, fluentBitConfig().Data) {
					t.Error("Fluent Bit config not restored")
				}
				wantRestarted(t, e)
			}},
		{"deleted plane object", func(e *testEnv) { e.api.del(planeCRPath) }, nil,
			func(t *testing.T, e *testEnv) {
				if _, ok := e.api.get(planeCRPath); !ok {
					t.Error("ObservabilityPlane not restored")
				}
			}},
		{"deleted chart policy", func(e *testEnv) { e.api.del(chartPolicy) }, []string{planeRelease}, nil},
		{"deleted log collector", func(e *testEnv) { e.api.del(daemonSetPath) }, []string{logsRelease}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEnv(t)
			e.installed(t)
			tt.break_(e)
			state, err := e.inst.Install(context.Background(), instance(Fingerprint()))
			if err != nil || !state.Ready {
				t.Fatalf("Install() = %+v, %v", state, err)
			}
			var got []string
			for _, c := range e.helm.calls {
				got = append(got, c[2])
			}
			if !slices.Equal(got, tt.wantHelm) {
				t.Errorf("helm releases = %v, want %v", got, tt.wantHelm)
			}
			if tt.check != nil {
				tt.check(t, e)
			}
		})
	}
}

// A pod not ready right after an install is waited out; once the settle
// window passes, only its release is re-run.
func TestInstall_SettlingIsWaitedOutThenRepaired(t *testing.T) {
	e := newTestEnv(t)
	e.installed(t)
	promIdx := slices.IndexFunc(workloads, func(w workload) bool { return w.what == "Prometheus" })
	promPod := fmt.Sprintf("%s/pod-%d", planePodsPath, promIdx)
	e.api.put(promPod, kubernetes.Object{Kind: "Pod", Metadata: kubernetes.ObjectMeta{Labels: workloads[promIdx].selector},
		Status: json.RawMessage(`{"conditions":[{"type":"Ready","status":"False"}]}`)})

	e.now = e.now.Add(time.Minute)
	state, err := e.inst.Install(context.Background(), instance(Fingerprint()))
	if err != nil || state.Ready || state.Message != "waiting for Prometheus to be ready" || len(e.helm.calls) != 0 {
		t.Fatalf("inside the window: %+v, %v, helm %v", state, err, e.helm.calls)
	}
	e.now = e.now.Add(settle)
	state, err = e.inst.Install(context.Background(), instance(Fingerprint()))
	if err != nil || !state.Ready {
		t.Fatalf("after the window: %+v, %v", state, err)
	}
	if len(e.helm.calls) != 1 || e.helm.calls[0][2] != prometheusRelease {
		t.Errorf("helm calls = %v, want only Prometheus", e.helm.calls)
	}
}

func TestInstall_MetricsReadiness(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(e *testEnv)
		wantReady bool
		wantMsg   string
	}{
		{"series returned", func(e *testEnv) {}, true, readyMessage},
		// Inside the settle window, so the first scrape may be still to
		// come; TestInstall_NoMetricsBeforeTheFirstScrape covers after it.
		{"empty series (RP-056)", func(e *testEnv) { e.api.metrics = `{"cpuUsage":[],"memoryUsage":[]}` }, false,
			"waiting for the first metrics to arrive; Prometheus scrapes every minute"},
		{"no memory series", func(e *testEnv) { e.api.metrics = `{}` }, false, ""},
		{"no Component pod", func(e *testEnv) { e.api.del(appPodPath) }, true, readyUncheckedMessage},
		{"only a young Component pod", func(e *testEnv) {
			e.api.put(appPodPath, componentPod("app-1", e.now.Add(-time.Minute)))
			e.api.metrics = `{}` // never asked
		}, true, readyUncheckedMessage},
		{"a pod missing a scope label is skipped", func(e *testEnv) {
			p := componentPod("app-1", e.now.Add(-time.Hour))
			delete(p.Metadata.Labels, labelEnvironment)
			e.api.put(appPodPath, p)
		}, true, readyUncheckedMessage},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEnv(t)
			e.installed(t)
			tt.setup(e)
			e.now = e.now.Add(time.Minute) // inside the settle window: no repair
			state, err := e.inst.Install(context.Background(), instance(Fingerprint()))
			if err != nil {
				t.Fatalf("Install() = %v", err)
			}
			if state.Ready != tt.wantReady || (tt.wantMsg != "" && state.Message != tt.wantMsg) {
				t.Errorf("state = %+v", state)
			}
			if len(e.helm.calls) != 0 {
				t.Errorf("helm calls = %v", e.helm.calls)
			}
		})
	}
}

// The readiness query is scoped by all four IDs, never by names alone
// (spike finding 5).
func TestInstall_MetricsQueryScopedByIDs(t *testing.T) {
	e := newTestEnv(t)
	e.installed(t)
	b := e.api.metricsBody
	scope, _ := b["searchScope"].(map[string]any)
	want := map[string]any{"namespace": "default", "projectUid": "p-uid", "componentUid": "c-uid", "environmentUid": "e-uid"}
	if b["metric"] != "resource" || fmt.Sprint(scope) != fmt.Sprint(want) {
		t.Errorf("query = %v", b)
	}
	from, _ := time.Parse(time.RFC3339, b["startTime"].(string))
	to, _ := time.Parse(time.RFC3339, b["endTime"].(string))
	if to.Sub(from) != probeWindow {
		t.Errorf("window = %v", to.Sub(from))
	}
}

func TestInstall_NoControlPlaneIsNotReady(t *testing.T) {
	e := newTestEnv(t)
	e.api.del(caSecretPath)
	_, err := e.inst.Install(context.Background(), instance(""))
	if !errors.Is(err, reconcile.ErrProviderNotReady) {
		t.Errorf("err = %v, want ErrProviderNotReady", err)
	}
	if len(e.helm.calls) != 0 {
		t.Errorf("helm ran: %v", e.helm.calls)
	}
}

// A release left locked by a killed Helm run is recovered, then installed.
func TestInstall_RecoversALockedRelease(t *testing.T) {
	for _, tt := range []struct{ status, wantVerb string }{
		{"pending-install", "uninstall"},
		{"pending-upgrade", "rollback"},
	} {
		t.Run(tt.status, func(t *testing.T) {
			e := newTestEnv(t)
			locked := true
			e.helm.respond = func(args []string) ([]byte, error) {
				switch {
				case args[0] == "upgrade" && args[2] == logsRelease && locked:
					return []byte("Error: " + helmBusy), errors.New("exit status 1")
				case args[0] == "status":
					return []byte(`WARNING: x` + "\n" + `{"info":{"status":"` + tt.status + `"}}`), nil
				case args[0] == tt.wantVerb:
					locked = false
				}
				return nil, nil
			}
			state, err := e.inst.Install(context.Background(), instance(""))
			if err != nil || !state.Ready {
				t.Fatalf("Install() = %+v, %v", state, err)
			}
			var logs []string
			for _, c := range e.helm.calls {
				if releaseArg(c) == logsRelease {
					logs = append(logs, c[0])
				}
			}
			if want := []string{"upgrade", "status", tt.wantVerb, "upgrade"}; !slices.Equal(logs, want) {
				t.Errorf("logs release calls = %v, want %v", logs, want)
			}
		})
	}
}

func TestUninstall_OrderAndTwiceIsClean(t *testing.T) {
	e := newTestEnv(t)
	e.installed(t)
	e.api.put(namespacePath, kubernetes.Object{Kind: "Namespace"})
	for k := range 2 {
		if err := e.inst.Uninstall(context.Background(), "pi-1"); err != nil {
			t.Fatalf("Uninstall() #%d = %v", k+1, err)
		}
	}
	var order []string
	for _, c := range e.api.timeline() {
		if strings.HasPrefix(c, "DELETE ") || strings.HasPrefix(c, "HELM ") {
			order = append(order, c)
		}
	}
	once := []string{
		"DELETE " + planeCRPath,
		"HELM uninstall " + metricsRelease,
		"HELM uninstall " + prometheusRelease,
		"HELM uninstall " + logsRelease,
		"HELM uninstall " + planeRelease,
		"DELETE " + namespacePath,
	}
	if want := append(slices.Clone(once), once...); !slices.Equal(order, want) {
		t.Errorf("order =\n  %s", strings.Join(order, "\n  "))
	}
	for _, c := range e.helm.calls {
		if !slices.Contains(c, "--ignore-not-found") {
			t.Errorf("uninstall without --ignore-not-found: %v", c)
		}
	}
	for _, p := range []string{planeCRPath, namespacePath} {
		if _, ok := e.api.get(p); ok {
			t.Errorf("%s left", p)
		}
	}
}

func TestUninstall_WaitsForTheNamespace(t *testing.T) {
	e := newTestEnv(t)
	e.api.put(namespacePath, kubernetes.Object{Kind: "Namespace"})
	e.api.keep[namespacePath] = true // terminating
	if err := e.inst.Uninstall(context.Background(), "pi-1"); !errors.Is(err, ErrNamespaceRemaining) {
		t.Fatalf("Uninstall() = %v, want ErrNamespaceRemaining", err)
	}
	e.api.keep[namespacePath] = false
	e.api.del(namespacePath)
	if err := e.inst.Uninstall(context.Background(), "pi-1"); err != nil {
		t.Errorf("Uninstall() once gone = %v", err)
	}
}

func TestPasswords(t *testing.T) {
	for _, tt := range []struct {
		p    string
		want bool
	}{
		{"Sd1-abc", true},
		{"abcdefghABCDEFGH12345678", false}, // alphanumeric: OpenObserve refuses it
		{"abc-123", false},
		{"ABC-123", false},
		{"Abc-def", false},
	} {
		if got := strongEnough(tt.p); got != tt.want {
			t.Errorf("strongEnough(%q) = %v", tt.p, got)
		}
	}
	seen := map[string]bool{}
	for range 50 {
		p, err := generatePassword()
		if err != nil || len(p) != passwordLength || !strongEnough(p) || seen[p] {
			t.Fatalf("generatePassword() = %q, %v", p, err)
		}
		seen[p] = true
	}
}

// The product's Prometheus values are the ones T003 measured (RP-056).
func TestPrometheusValuesMatchTheBench(t *testing.T) {
	bench, err := os.ReadFile("../../../profiles/edge-1/bench/observability-load/prometheus-plain-values.yaml")
	if err != nil {
		t.Fatal(err)
	}
	norm := func(b []byte) []byte { return bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n")) }
	if !bytes.Equal(norm(bench), norm(prometheusValues)) {
		t.Error("prometheus-values.yaml differs from the bench's prometheus-plain-values.yaml; change both together")
	}
}

func TestFingerprint_StableAndCoversValues(t *testing.T) {
	if !strings.HasPrefix(Fingerprint(), "sha256:") || Fingerprint() != fingerprint() {
		t.Errorf("Fingerprint() = %q", Fingerprint())
	}
	if !strings.Contains(string(prometheusValues), "servicePort: 9091") {
		t.Error("the values lost the adapter's Prometheus port")
	}
}
