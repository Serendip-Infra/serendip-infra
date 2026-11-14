package openchoreo

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/serendip-infra/serendip-infra/core/reconcile"
	"github.com/serendip-infra/serendip-infra/core/resource"
	"github.com/serendip-infra/serendip-infra/providers/internal/helm"
	"github.com/serendip-infra/serendip-infra/providers/kubernetes"
)

const (
	controlPlaneNamespace = "openchoreo-control-plane"
	apiVersion            = "openchoreo.dev/v1alpha1"
	// PlaneName is the namespaced ObservabilityPlane a namespaced
	// DataPlane falls back to by name, so the data plane's spec is never
	// edited (spike finding 2).
	PlaneName = "default"

	agentTLSSecretName   = "cluster-agent-tls"
	clusterGatewayCAName = "cluster-gateway-ca"
	clusterGatewayCAKey  = "ca.crt"

	fluentBitDaemonSet = "fluent-bit"

	metricsAdapterService = "metrics-adapter"
	metricsAdapterPort    = 9099
)

const observabilityPlanesPath = "/apis/openchoreo.dev/v1alpha1/namespaces/" + controlPlaneNamespace + "/observabilityplanes"

// settle is how long after an install a check that may only be settling
// is waited out rather than repaired: the stores start slowly, and the
// adapters, kube-state-metrics and Fluent Bit crash-loop until they're up
// (spike finding 15). Each repair that didn't fix it doubles the wait, up
// to maxSettle, so a lasting fault doesn't run Helm every few minutes.
const (
	settle    = 5 * time.Minute
	maxSettle = 30 * time.Minute
)

// probeAge is how long a Component's pod must have run before the
// readiness query expects series for it: a scrape every minute, and the
// query asks for the last 10.
const (
	probeAge    = 5 * time.Minute
	probeWindow = 10 * time.Minute
)

const readyMessage = "logs and metrics are being collected; logs are kept 3 days, metrics 3 days or 2 GiB"

// readyUncheckedMessage is readyMessage before any Component has run long
// enough for the metrics query to be checked against it.
const readyUncheckedMessage = readyMessage + " (metrics are checked against a Component once one has run for 5 minutes)"

// managedBy labels the objects Serendip hand-builds here.
var managedBy = map[string]string{"app.kubernetes.io/managed-by": "serendip"}

// ErrNamespaceRemaining is Uninstall's answer while the plane's namespace
// (its volumes, Secrets and jobs) is still being deleted. The clean-up
// retries until it's gone.
var ErrNamespaceRemaining = errors.New("the observability namespace is still being deleted")

// ErrPlaneRemaining is Uninstall's answer while ObservabilityPlane/default
// is still being deleted, before anything else is removed.
var ErrPlaneRemaining = errors.New("the observability plane's registration is still being deleted")

// Installer installs and removes the observability package (plan
// decisions 1, 12 and 13). It's a reconcile.PackageInstaller. There's one
// instance per capability (RP-033), so its settling state isn't kept per
// instance, and core runs one package job at a time (packagejob.go), so
// Install and Uninstall never run at once.
type Installer struct {
	run    helm.Runner
	client *kubernetes.Client
	now    func() time.Time

	mu sync.Mutex
	// lastInstall is when this process last applied the package or, after
	// a restart, when it first found it already applied: a restarted node
	// brings every pod up again, and that is waited out once rather than
	// met with a 15-minute Helm run. repairs counts settling faults
	// repaired in a row since the checks last passed.
	lastInstall time.Time
	repairs     int
	// probe is the pod the metrics check last used (probePod).
	probe *probePod
}

var _ reconcile.PackageInstaller = (*Installer)(nil)

// NewInstaller builds an Installer, failing fast if `helm` isn't on
// $PATH, a host prerequisite (ADR 0010).
func NewInstaller(client *kubernetes.Client, kubeconfigPath string) (*Installer, error) {
	if err := helm.CheckAvailable(); err != nil {
		return nil, fmt.Errorf("openchoreo observability: %w", err)
	}
	return newInstaller(helm.WithKubeconfig(helm.Run, kubeconfigPath), client), nil
}

func newInstaller(run helm.Runner, client *kubernetes.Client) *Installer {
	return &Installer{run: run, client: client, now: time.Now}
}

// observabilityPlane registers the plane, trusting the chart's cluster
// agent certificate, like the workflow plane's.
func observabilityPlane() kubernetes.Object {
	return kubernetes.Object{
		APIVersion: apiVersion, Kind: "ObservabilityPlane",
		Metadata: kubernetes.ObjectMeta{Name: PlaneName, Namespace: controlPlaneNamespace, Labels: managedBy},
		Spec: mustJSON(map[string]any{
			"planeID":     PlaneName,
			"observerURL": observerURL,
			"clusterAgent": map[string]any{"clientCA": map[string]any{"secretKeyRef": map[string]string{
				"name": agentTLSSecretName, "namespace": PlaneNamespace, "key": clusterGatewayCAKey,
			}}},
		}),
	}
}

// observerSecret is the placeholder the plane chart needs (spike finding
// 2): with security off the UID resolver's OAuth client is never used.
func observerSecret() kubernetes.Object {
	return kubernetes.Object{
		APIVersion: "v1", Kind: "Secret",
		Metadata: kubernetes.ObjectMeta{Name: observerSecretName, Namespace: PlaneNamespace, Labels: managedBy},
		Data:     mustJSON(map[string][]byte{"UID_RESOLVER_OAUTH_CLIENT_SECRET": []byte("unused-security-disabled")}),
	}
}

// Fingerprint identifies what a full install applies: every release's
// Helm arguments, the Prometheus values, and the hand-built objects. When
// an instance's status.applied equals it, Install only checks (plan
// decision 12). The nodes' addresses aren't in it: they change without
// any install, so the checks compare them and repair the rule instead.
// The generated password and the copied CA aren't in it either.
func Fingerprint() string { return fingerprint() }

var fingerprint = sync.OnceValue(func() string {
	var args [][]string
	for _, r := range releases {
		args = append(args, r.installArgs())
	}
	values := sha256.Sum256(prometheusValues)
	b := mustJSON(struct {
		Helm    [][]string          `json:"helm"`
		Values  string              `json:"values"`
		Objects []kubernetes.Object `json:"objects"`
	}{args, hex.EncodeToString(values[:]), []kubernetes.Object{observabilityPlane(), observerSecret(), nodeRule(nil)}})
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
})

// allReleases, as a problem's release, repairs by running every release.
const allReleases = "*"

// problem is what a quick check found wrong.
type problem struct {
	message string
	// settling: it may only be still coming up after an install.
	settling bool
	// release is the Helm release a repair runs: one name, allReleases,
	// or "" to re-apply only the hand-built objects.
	release string
	// reportOnly: no repair can fix it (a query that answers but finds no
	// data, RP-056), so it's reported and nothing is re-applied.
	reportOnly bool
}

// Install brings the package towards installed. With a matching
// fingerprint and passing checks it runs no Helm and applies nothing. A
// settling check soon after an install is waited out; anything else is
// repaired, with Helm only for the release at fault.
func (i *Installer) Install(ctx context.Context, inst resource.ProviderInstance) (reconcile.PackageState, error) {
	want := fingerprint()
	if inst.Status.Applied != want {
		return i.install(ctx, want, allReleases)
	}
	i.mu.Lock()
	if i.lastInstall.IsZero() {
		i.lastInstall = i.now()
	}
	i.mu.Unlock()
	p, msg, err := i.check(ctx)
	if err != nil {
		return reconcile.PackageState{}, err
	}
	if p == nil {
		i.resetRepairs()
		return reconcile.PackageState{Ready: true, Message: msg, Applied: want}, nil
	}
	if p.reportOnly || (p.settling && i.waitOut()) {
		return reconcile.PackageState{Message: p.message, Applied: want}, nil
	}
	return i.install(ctx, want, p.release)
}

func (i *Installer) resetRepairs() {
	i.mu.Lock()
	i.repairs = 0
	i.mu.Unlock()
}

// waitOut reports whether a settling fault is still inside its settle
// window, counting a repair when it isn't.
func (i *Installer) waitOut() bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.lastInstall.IsZero() {
		return false
	}
	if i.now().Sub(i.lastInstall) < min(settle<<i.repairs, maxSettle) {
		return true
	}
	if settle<<i.repairs < maxSettle {
		i.repairs++
	}
	return false
}

// install applies the package in plan decision 1's order, as amended by
// T003 (ADR 0021): the namespace, the CA trust copy, the placeholder and
// admin Secrets, the plane chart, the node rule (before the logs module:
// T003 finding 1), the logs module, plain Prometheus, the metrics
// module's adapter, then the ObservabilityPlane. Helm runs for the
// release named by which (allReleases for every one, "" for none).
func (i *Installer) install(ctx context.Context, want, which string) (reconcile.PackageState, error) {
	if err := i.applyObjects(ctx, func(ctx context.Context) error {
		return i.helmReleases(ctx, which, planeRelease)
	}); err != nil {
		return reconcile.PackageState{}, err
	}
	if err := i.helmReleases(ctx, which, logsRelease, prometheusRelease, metricsRelease); err != nil {
		return reconcile.PackageState{}, err
	}
	if _, err := apply(ctx, i.client, observabilityPlanesPath, observabilityPlane()); err != nil {
		return reconcile.PackageState{}, fmt.Errorf("openchoreo observability: %w", err)
	}
	i.mu.Lock()
	i.lastInstall = i.now()
	i.mu.Unlock()

	p, msg, err := i.check(ctx)
	if err != nil {
		return reconcile.PackageState{}, err
	}
	if p != nil {
		return reconcile.PackageState{Message: p.message, Applied: want}, nil
	}
	i.resetRepairs()
	return reconcile.PackageState{Ready: true, Message: msg, Applied: want}, nil
}

// applyObjects applies the hand-built objects that come before the logs
// module, running plane (the plane chart's install) after the Secrets it
// needs and before the node rule.
func (i *Installer) applyObjects(ctx context.Context, plane func(context.Context) error) error {
	if err := ensureNamespace(ctx, i.client, PlaneNamespace); err != nil {
		return fmt.Errorf("openchoreo observability: %w", err)
	}
	if err := copyClusterGatewayCA(ctx, i.client); err != nil {
		return err
	}
	if _, err := apply(ctx, i.client, secretsPath, observerSecret()); err != nil {
		return fmt.Errorf("openchoreo observability: %w", err)
	}
	if err := ensureAdminCredentials(ctx, i.client); err != nil {
		return fmt.Errorf("openchoreo observability: %w", err)
	}
	if err := plane(ctx); err != nil {
		return err
	}
	ips, err := nodeIPs(ctx, i.client)
	if err != nil {
		return fmt.Errorf("openchoreo observability: %w", err)
	}
	if _, err := apply(ctx, i.client, networkPoliciesPath, nodeRule(ips)); err != nil {
		return fmt.Errorf("openchoreo observability: %w", err)
	}
	return nil
}

// helmReleases runs, in order, each of names that which selects.
func (i *Installer) helmReleases(ctx context.Context, which string, names ...string) error {
	for _, name := range names {
		if which != allReleases && which != name {
			continue
		}
		r, _ := releaseNamed(name)
		if err := i.helmInstall(ctx, r); err != nil {
			return fmt.Errorf("openchoreo observability: install %s: %w", r.what, err)
		}
	}
	return nil
}

// helmBusy is Helm's answer while a release is locked by an operation
// that hasn't finished.
const helmBusy = "another operation (install/upgrade/rollback) is in progress"

// helmInstall runs r's install. A release left locked mid-way (a process
// killed while Helm ran) is recovered first, then installed again, as for
// builds: Serendip runs one package job at a time, so a lock it finds
// isn't its own live one (RP-045).
func (i *Installer) helmInstall(ctx context.Context, r release) error {
	out, err := i.runInstall(ctx, r)
	if err == nil || !strings.Contains(string(out)+err.Error(), helmBusy) {
		return err
	}
	if rerr := i.recoverPending(ctx, r.name); rerr != nil {
		return fmt.Errorf("%w (%s: %s); recovering the release left mid-operation: %v", err, r.name, helmBusy, rerr)
	}
	_, err = i.runInstall(ctx, r)
	return err
}

// runInstall runs r's install arguments, writing the values file it
// needs, if any, for the run's length.
func (i *Installer) runInstall(ctx context.Context, r release) ([]byte, error) {
	args := r.installArgs()
	if r.valuesFile {
		f, err := os.CreateTemp("", "serendip-prometheus-values-*.yaml")
		if err != nil {
			return nil, fmt.Errorf("write the Prometheus values: %w", err)
		}
		defer os.Remove(f.Name())
		_, werr := f.Write(prometheusValues)
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			return nil, fmt.Errorf("write the Prometheus values: %w", werr)
		}
		args[slices.Index(args, valuesFileArg)] = f.Name()
	}
	return i.run(ctx, args...)
}

// recoverPending unlocks a release an unfinished operation left pending:
// a first install that never finished is uninstalled, and an upgrade or
// rollback is rolled back to the last release.
func (i *Installer) recoverPending(ctx context.Context, name string) error {
	out, err := i.run(ctx, "status", name, "--namespace", PlaneNamespace, "-o", "json")
	if err != nil {
		return err
	}
	var st struct {
		Info struct {
			Status string `json:"status"`
		} `json:"info"`
	}
	if start := bytes.IndexByte(out, '{'); start < 0 || json.Unmarshal(out[start:], &st) != nil {
		return fmt.Errorf("read the release's status: %.200s", out)
	}
	switch st.Info.Status {
	case "pending-install":
		log.Printf("openchoreo observability: release %s was left %s; uninstalling it", name, st.Info.Status)
		_, err = i.run(ctx, "uninstall", name, "--namespace", PlaneNamespace, "--wait", "--timeout", helmUninstallTimeout)
	case "pending-upgrade", "pending-rollback":
		log.Printf("openchoreo observability: release %s was left %s; rolling it back", name, st.Info.Status)
		_, err = i.run(ctx, "rollback", name, "--namespace", PlaneNamespace, "--wait", "--timeout", helmInstallTimeout)
	default:
		err = fmt.Errorf("the release is %q, not pending", st.Info.Status)
	}
	return err
}

// workload is one part of the package the checks expect a ready pod for.
type workload struct {
	what     string
	selector map[string]string
	release  string
}

// workloads are the charts' pods, by their selectors (the charts' own,
// at the versions in VERSIONS.md). The logs store's is the node rule's,
// so its check also shows that rule still selects a pod.
var workloads = []workload{
	{"the Observer", map[string]string{"app.kubernetes.io/instance": planeRelease, "app.kubernetes.io/component": "observer"}, planeRelease},
	{"the observability plane's agent", map[string]string{"app.kubernetes.io/instance": planeRelease, "app.kubernetes.io/component": "cluster-agent"}, planeRelease},
	{"the observability plane's controller", map[string]string{"app.kubernetes.io/instance": planeRelease, "app.kubernetes.io/component": "controller-manager"}, planeRelease},
	{"the logs store", logsStoreSelector, logsRelease},
	{"the logs adapter", map[string]string{"app": "logs-adapter-openobserve"}, logsRelease},
	{"Prometheus", map[string]string{"app.kubernetes.io/instance": prometheusRelease, "app.kubernetes.io/name": "prometheus", "app.kubernetes.io/component": "server"}, prometheusRelease},
	{"kube-state-metrics", map[string]string{"app.kubernetes.io/instance": prometheusRelease, "app.kubernetes.io/name": "kube-state-metrics"}, prometheusRelease},
	{"the metrics adapter", map[string]string{"app": "metrics-adapter-prometheus"}, metricsRelease},
}

const (
	secretsPath   = "/api/v1/namespaces/" + PlaneNamespace + "/secrets"
	planePodsPath = "/api/v1/namespaces/" + PlaneNamespace + "/pods"
	daemonSetPath = "/apis/apps/v1/namespaces/" + PlaneNamespace + "/daemonsets/" + fluentBitDaemonSet
)

// check runs the quick checks (plan decision 12): six GETs and one
// metrics query. Every workload has a ready pod, Fluent Bit runs on every
// node it should, both policies are there and the node rule admits the
// nodes' current addresses, the plane is registered, and a real metrics
// query returns series for a running Component (RP-056). It returns nil
// and the ready message when all is well; err is for a failed request.
func (i *Installer) check(ctx context.Context) (*problem, string, error) {
	pods, err := i.client.List(ctx, planePodsPath)
	if err != nil {
		return nil, "", fmt.Errorf("openchoreo observability: list pods: %w", err)
	}
	for _, w := range workloads {
		if !anyReady(pods, w.selector) {
			return &problem{message: "waiting for " + w.what + " to be ready", settling: true, release: w.release}, "", nil
		}
	}

	if p, err := i.checkFluentBit(ctx); p != nil || err != nil {
		return p, "", err
	}
	if p, err := i.checkPolicies(ctx); p != nil || err != nil {
		return p, "", err
	}

	if _, err := i.client.Get(ctx, observabilityPlanesPath+"/"+PlaneName); errors.Is(err, kubernetes.ErrNotFound) {
		return &problem{message: "the observability plane isn't registered"}, "", nil
	} else if err != nil {
		return nil, "", fmt.Errorf("openchoreo observability: get ObservabilityPlane: %w", err)
	}

	return i.checkMetrics(ctx)
}

func (i *Installer) checkFluentBit(ctx context.Context) (*problem, error) {
	ds, err := i.client.Get(ctx, daemonSetPath)
	if errors.Is(err, kubernetes.ErrNotFound) {
		return &problem{message: "the log collector is missing", release: logsRelease}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("openchoreo observability: get daemonset %s: %w", fluentBitDaemonSet, err)
	}
	var st struct {
		Desired int `json:"desiredNumberScheduled"`
		Ready   int `json:"numberReady"`
	}
	if len(ds.Status) > 0 {
		if err := json.Unmarshal(ds.Status, &st); err != nil {
			return nil, fmt.Errorf("openchoreo observability: decode daemonset %s status: %w", fluentBitDaemonSet, err)
		}
	}
	if st.Desired == 0 {
		return &problem{message: "waiting for the log collector to be scheduled on the nodes", settling: true, release: logsRelease}, nil
	}
	if st.Ready < st.Desired {
		return &problem{message: fmt.Sprintf("waiting for the log collector to run on every node (%d of %d ready)", st.Ready, st.Desired),
			settling: true, release: logsRelease}, nil
	}
	return nil, nil
}

// checkPolicies checks both policies are there and the node rule admits
// exactly the nodes' current addresses. A changed address is repaired by
// re-applying the rule, with no Helm run.
func (i *Installer) checkPolicies(ctx context.Context) (*problem, error) {
	policies, err := i.client.List(ctx, networkPoliciesPath)
	if err != nil {
		return nil, fmt.Errorf("openchoreo observability: list network policies: %w", err)
	}
	var chart, rule *kubernetes.Object
	for k := range policies {
		switch policies[k].Metadata.Name {
		case chartPolicyName:
			chart = &policies[k]
		case NodeRuleName:
			rule = &policies[k]
		}
	}
	if chart == nil {
		return &problem{message: "the observability plane's network policy is missing", release: planeRelease}, nil
	}
	if rule == nil {
		return &problem{message: "the log collector's network rule is missing"}, nil
	}
	ips, err := nodeIPs(ctx, i.client)
	if err != nil {
		return nil, fmt.Errorf("openchoreo observability: %w", err)
	}
	if !ruleMatches(*rule, ips) {
		return &problem{message: "the log collector's network rule doesn't match the nodes' addresses"}, nil
	}
	return nil, nil
}

// probePod is a running Component's pod, with the IDs a metrics query is
// scoped by (spike finding 5: scopes are IDs, and a missing one widens the
// query).
type probePod struct {
	// podNamespace and name address the pod; the rest is its scope, where
	// namespace is OpenChoreo's (a label), not the pod's.
	podNamespace, name                         string
	namespace, project, component, environment string
}

// Labels OpenChoreo puts on a Component's pods, which kube-state-metrics
// exports and the adapter's queries join on.
const (
	labelNamespace   = "openchoreo.dev/namespace"
	labelProject     = "openchoreo.dev/project-uid"
	labelComponent   = "openchoreo.dev/component-uid"
	labelEnvironment = "openchoreo.dev/environment-uid"
)

// checkMetrics runs the adapter's resource query for the longest-running
// Component pod, through the API server's Service proxy (plan decision
// 4), and expects memory series for it. Empty series mean Prometheus no
// longer matches what the adapter queries (RP-056), or hasn't scraped
// yet. With no Component running long enough, there's nothing to check
// against yet, which isn't a fault.
func (i *Installer) checkMetrics(ctx context.Context) (*problem, string, error) {
	pod, ok, err := i.probePod(ctx)
	if err != nil {
		return nil, "", err
	}
	if !ok {
		return nil, readyUncheckedMessage, nil
	}
	now := i.now().UTC()
	body := map[string]any{
		"metric":    "resource",
		"startTime": now.Add(-probeWindow).Format(time.RFC3339),
		"endTime":   now.Format(time.RFC3339),
		"step":      "1m",
		"searchScope": map[string]string{
			"namespace": pod.namespace, "projectUid": pod.project,
			"componentUid": pod.component, "environmentUid": pod.environment,
		},
	}
	path := kubernetes.ServiceProxyPath(PlaneNamespace, metricsAdapterService, metricsAdapterPort) + "/api/v1/metrics/query"
	raw, err := i.client.PostRaw(ctx, path, body)
	var se *kubernetes.StatusError
	if errors.As(err, &se) || errors.Is(err, kubernetes.ErrNotFound) {
		// The proxy or the adapter answered with an error: the adapter's
		// pod was ready a moment ago, so it's likely still starting.
		return &problem{message: fmt.Sprintf("the metrics query failed: %v", trimErr(err)), settling: true, release: metricsRelease}, "", nil
	}
	if err != nil {
		return nil, "", fmt.Errorf("openchoreo observability: metrics query: %w", err)
	}
	var series map[string]json.RawMessage
	if err := json.Unmarshal(raw, &series); err != nil || !hasPoints(series["memoryUsage"]) {
		return &problem{message: fmt.Sprintf(
			"metrics queries return no data for pod %s; Prometheus may not match what the metrics adapter queries (see the package's VERSIONS.md)", pod.name),
			reportOnly: true}, "", nil
	}
	return nil, readyMessage, nil
}

// oldestProbe picks the longest-running pod that started before cutoff,
// is ready, and carries every scope label.
func oldestProbe(pods []kubernetes.Object, cutoff time.Time) (probePod, bool) {
	var best probePod
	var bestStart time.Time
	for _, p := range pods {
		pp, start, ok := eligibleProbe(p, cutoff)
		if ok && (bestStart.IsZero() || start.Before(bestStart)) {
			best, bestStart = pp, start
		}
	}
	return best, !bestStart.IsZero()
}

// eligibleProbe reads p as a probe: ready, started before cutoff, and
// carrying every scope label.
func eligibleProbe(p kubernetes.Object, cutoff time.Time) (probePod, time.Time, bool) {
	l := p.Metadata.Labels
	pp := probePod{p.Metadata.Namespace, p.Metadata.Name, l[labelNamespace], l[labelProject], l[labelComponent], l[labelEnvironment]}
	if pp.podNamespace == "" || pp.namespace == "" || pp.project == "" || pp.component == "" || pp.environment == "" || !podReady(p) {
		return probePod{}, time.Time{}, false
	}
	var st struct {
		StartTime time.Time `json:"startTime"`
	}
	if json.Unmarshal(p.Status, &st) != nil || st.StartTime.IsZero() || st.StartTime.After(cutoff) {
		return probePod{}, time.Time{}, false
	}
	return pp, st.StartTime, true
}

// probePod finds the pod the metrics query is checked against. The one
// used last time is read back by name, one small GET, and the cluster's
// Component pods are listed only when it's gone or no longer eligible, so
// a resync costs the same however many Components run.
func (i *Installer) probePod(ctx context.Context) (probePod, bool, error) {
	cutoff := i.now().Add(-probeAge)
	i.mu.Lock()
	last := i.probe
	i.mu.Unlock()
	if last != nil {
		obj, err := i.client.Get(ctx, "/api/v1/namespaces/"+last.podNamespace+"/pods/"+last.name)
		if err != nil && !errors.Is(err, kubernetes.ErrNotFound) {
			return probePod{}, false, fmt.Errorf("openchoreo observability: get pod %s/%s: %w", last.podNamespace, last.name, err)
		}
		if err == nil {
			if pp, _, ok := eligibleProbe(obj, cutoff); ok {
				return pp, true, nil
			}
		}
	}
	q := url.Values{"labelSelector": {labelComponent}, "fieldSelector": {"status.phase=Running"}}
	pods, err := i.client.List(ctx, "/api/v1/pods?"+q.Encode())
	if err != nil {
		return probePod{}, false, fmt.Errorf("openchoreo observability: list Component pods: %w", err)
	}
	pp, ok := oldestProbe(pods, cutoff)
	i.mu.Lock()
	if ok {
		i.probe = &pp
	} else {
		i.probe = nil
	}
	i.mu.Unlock()
	return pp, ok, nil
}

// hasPoints reports whether a series is a list holding at least one point
// that isn't null or empty. The point's own shape is T006's.
func hasPoints(v json.RawMessage) bool {
	var points []json.RawMessage
	if json.Unmarshal(v, &points) != nil {
		return false
	}
	for _, p := range points {
		if s := strings.TrimSpace(string(p)); s != "null" && s != "{}" && s != "[]" && s != `""` {
			return true
		}
	}
	return false
}

func trimErr(err error) string {
	var se *kubernetes.StatusError
	if errors.As(err, &se) {
		return fmt.Sprintf("status %d", se.Code)
	}
	s := []rune(err.Error())
	if len(s) > 200 {
		s = s[:200]
	}
	return string(s)
}

// anyReady reports whether a pod matching selector is ready.
func anyReady(pods []kubernetes.Object, selector map[string]string) bool {
	for _, p := range pods {
		if matches(p.Metadata.Labels, selector) && podReady(p) {
			return true
		}
	}
	return false
}

func matches(labels, selector map[string]string) bool {
	for k, v := range selector {
		if labels[k] != v {
			return false
		}
	}
	return true
}

func podReady(p kubernetes.Object) bool {
	if p.Metadata.DeletionTimestamp != "" {
		return false
	}
	var st struct {
		Conditions []struct {
			Type   string `json:"type"`
			Status string `json:"status"`
		} `json:"conditions"`
	}
	if json.Unmarshal(p.Status, &st) != nil {
		return false
	}
	for _, c := range st.Conditions {
		if c.Type == "Ready" {
			return c.Status == "True"
		}
	}
	return false
}

// Uninstall removes the package in plan decision 13's order, as amended
// by T001: the ObservabilityPlane, the node rule, the releases in reverse,
// then the namespace, which takes the volumes (every stored log and
// metric), the Secrets, the CA copy and the setup job with it. Every step
// treats "already gone" as done, so it's idempotent, and safe for an
// instance that never installed anything. It returns
// ErrNamespaceRemaining until the namespace is gone. The plane chart's
// small observabilityalertrules CRD is left, as RP-039 left Argo's; plain
// Prometheus installs none (ADR 0021).
func (i *Installer) Uninstall(ctx context.Context, instanceID string) error {
	if err := deleteIfExists(ctx, i.client, observabilityPlanesPath+"/"+PlaneName); err != nil {
		return fmt.Errorf("openchoreo observability: %w", err)
	}
	// A finalizer on it is cleared by OpenChoreo's controllers, so it must
	// be gone before the plane's own controller is, or it's left
	// terminating outside the namespace this removes.
	if _, err := i.client.Get(ctx, observabilityPlanesPath+"/"+PlaneName); err == nil {
		return fmt.Errorf("openchoreo observability: %w", ErrPlaneRemaining)
	} else if !errors.Is(err, kubernetes.ErrNotFound) {
		return fmt.Errorf("openchoreo observability: get ObservabilityPlane: %w", err)
	}
	if err := deleteIfExists(ctx, i.client, networkPoliciesPath+"/"+NodeRuleName); err != nil {
		return fmt.Errorf("openchoreo observability: %w", err)
	}
	for _, r := range slices.Backward(releases) {
		if _, err := i.run(ctx,
			"uninstall", r.name, "--namespace", PlaneNamespace,
			"--ignore-not-found", "--wait", "--timeout", helmUninstallTimeout,
		); err != nil {
			return fmt.Errorf("openchoreo observability: uninstall %s: %w", r.what, err)
		}
	}
	nsPath := "/api/v1/namespaces/" + PlaneNamespace
	if err := deleteIfExists(ctx, i.client, nsPath); err != nil {
		return fmt.Errorf("openchoreo observability: %w", err)
	}
	if _, err := i.client.Get(ctx, nsPath); err == nil {
		return fmt.Errorf("openchoreo observability: %w", ErrNamespaceRemaining)
	} else if !errors.Is(err, kubernetes.ErrNotFound) {
		return fmt.Errorf("openchoreo observability: get namespace: %w", err)
	}
	return nil
}

// ensureAdminCredentials creates the logs store's admin Secret once, with
// a generated password, and leaves an existing one alone, so a resync or
// repair never changes it (plan decision 1.2). It lives only in the
// cluster: never in Serendip's store, API or logs.
func ensureAdminCredentials(ctx context.Context, client *kubernetes.Client) error {
	existing, err := client.Get(ctx, secretsPath+"/"+adminSecretName)
	if err == nil {
		var data map[string][]byte
		if json.Unmarshal(existing.Data, &data) != nil || len(data["ZO_ROOT_USER_EMAIL"]) == 0 || len(data["ZO_ROOT_USER_PASSWORD"]) == 0 {
			// Not replaced: the store's data was written under the
			// credentials it had, so a person decides.
			return fmt.Errorf("the %s secret lacks ZO_ROOT_USER_EMAIL or ZO_ROOT_USER_PASSWORD; restore them, or delete the secret to have new ones generated", adminSecretName)
		}
		return nil
	}
	if !errors.Is(err, kubernetes.ErrNotFound) {
		return fmt.Errorf("get %s secret: %w", adminSecretName, err)
	}
	password, err := generatePassword()
	if err != nil {
		return err
	}
	_, err = client.Create(ctx, secretsPath, kubernetes.Object{
		APIVersion: "v1", Kind: "Secret",
		Metadata: kubernetes.ObjectMeta{Name: adminSecretName, Namespace: PlaneNamespace, Labels: managedBy},
		Data: mustJSON(map[string][]byte{
			"ZO_ROOT_USER_EMAIL":    []byte(adminEmail),
			"ZO_ROOT_USER_PASSWORD": []byte(password),
		}),
	})
	var se *kubernetes.StatusError
	if errors.As(err, &se) && se.Code == http.StatusConflict {
		return nil // created meanwhile; that one is kept
	}
	if err != nil {
		return fmt.Errorf("create %s secret: %w", adminSecretName, err)
	}
	return nil
}

const (
	passwordLength  = 32
	passwordSymbols = "-_.~!"
	passwordChars   = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789" + passwordSymbols
)

// generatePassword returns a random password with a lower and an upper
// case letter, a digit and a symbol: OpenObserve refuses one without them
// (T003 finding 2). About 190 bits.
func generatePassword() (string, error) {
	for {
		b := make([]byte, passwordLength)
		for k := range b {
			n, err := rand.Int(rand.Reader, big.NewInt(int64(len(passwordChars))))
			if err != nil {
				return "", fmt.Errorf("generate a password: %w", err)
			}
			b[k] = passwordChars[n.Int64()]
		}
		if p := string(b); strongEnough(p) {
			return p, nil
		}
	}
}

func strongEnough(p string) bool {
	return strings.ContainsAny(p, "abcdefghijklmnopqrstuvwxyz") &&
		strings.ContainsAny(p, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") &&
		strings.ContainsAny(p, "0123456789") &&
		strings.ContainsAny(p, passwordSymbols)
}

// copyClusterGatewayCA copies the control plane's real CA into a
// cluster-gateway-ca ConfigMap in PlaneNamespace, which the plane chart's
// agent mounts to trust the control plane: the same fix the workflow
// plane needs. Without the control plane, it reports
// reconcile.ErrProviderNotReady.
func copyClusterGatewayCA(ctx context.Context, client *kubernetes.Client) error {
	src, err := client.Get(ctx, fmt.Sprintf("/api/v1/namespaces/%s/secrets/%s", controlPlaneNamespace, clusterGatewayCAName))
	if errors.Is(err, kubernetes.ErrNotFound) {
		return fmt.Errorf("openchoreo observability: the control plane's CA isn't there yet: %w", reconcile.ErrProviderNotReady)
	}
	if err != nil {
		return fmt.Errorf("openchoreo observability: get %s/%s secret: %w", controlPlaneNamespace, clusterGatewayCAName, err)
	}
	var data map[string][]byte
	if err := json.Unmarshal(src.Data, &data); err != nil {
		return fmt.Errorf("openchoreo observability: decode %s/%s secret: %w", controlPlaneNamespace, clusterGatewayCAName, err)
	}
	ca, ok := data[clusterGatewayCAKey]
	if !ok {
		return fmt.Errorf("openchoreo observability: %s/%s secret has no %s", controlPlaneNamespace, clusterGatewayCAName, clusterGatewayCAKey)
	}
	_, err = apply(ctx, client, fmt.Sprintf("/api/v1/namespaces/%s/configmaps", PlaneNamespace), kubernetes.Object{
		APIVersion: "v1", Kind: "ConfigMap",
		Metadata: kubernetes.ObjectMeta{Name: clusterGatewayCAName, Namespace: PlaneNamespace},
		Data:     mustJSON(map[string]string{clusterGatewayCAKey: string(ca)}),
	})
	if err != nil {
		return fmt.Errorf("openchoreo observability: %w", err)
	}
	return nil
}

// ensureNamespace server-side-applies namespace with no fields beyond its
// name, so it never contends with Helm's --create-namespace.
func ensureNamespace(ctx context.Context, client *kubernetes.Client, namespace string) error {
	_, err := apply(ctx, client, "/api/v1/namespaces", kubernetes.Object{
		APIVersion: "v1", Kind: "Namespace",
		Metadata: kubernetes.ObjectMeta{Name: namespace},
	})
	return err
}

func apply(ctx context.Context, client *kubernetes.Client, collection string, obj kubernetes.Object) (kubernetes.Object, error) {
	applied, err := client.Apply(ctx, collection+"/"+obj.Metadata.Name, obj, kubernetes.FieldManager)
	if err != nil {
		return kubernetes.Object{}, fmt.Errorf("apply %s %s: %w", obj.Kind, obj.Metadata.Name, err)
	}
	return applied, nil
}

// deleteIfExists deletes path, treating "already gone" as done.
func deleteIfExists(ctx context.Context, client *kubernetes.Client, path string) error {
	if err := client.Delete(ctx, path); err != nil && !errors.Is(err, kubernetes.ErrNotFound) {
		return fmt.Errorf("delete %s: %w", path, err)
	}
	return nil
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("openchoreo observability: marshal static object: %v", err))
	}
	return b
}
