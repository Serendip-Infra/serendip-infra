package openchoreo

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/serendip-infra/serendip-infra/core/reconcile"
	"github.com/serendip-infra/serendip-infra/core/resource"
	"github.com/serendip-infra/serendip-infra/providers/internal/helm"
	"github.com/serendip-infra/serendip-infra/providers/kubernetes"
)

// The tracing package (specs/025-networking plan decision 10): OpenChoreo's
// tracing module's OTel Collector, writing to the observability package's
// logs store, so removing it leaves logs and metrics. Its bundled store,
// its tail sampling and its adapter are off: the adapter can't scope by
// UIDs or names (T001), so traces are read with SQL (tracing_read.go).
// The collector runs Serendip's own config (tracing-collector.yaml), which
// takes identity from the connection only.

const (
	tracingRelease = "observability-tracing-openobserve"
	// collectorConfigName is the ConfigMap holding Serendip's collector
	// config, under the key the chart's Deployment mounts.
	collectorConfigName = "serendip-trace-collector"
	collectorConfigKey  = "relay"
	// collectorService is the chart's OTLP Service (VERSIONS.md).
	collectorService = "opentelemetry-collector"
	// tracesStream is the store's stream the collector writes, of type
	// traces (VERSIONS.md).
	tracesStream = "default"
)

// OTLPEndpoint is where workloads send traces while the package is Ready
// (plan decision 11): the collector's OTLP/HTTP port, in-cluster.
// cmd/serendipd passes it to the application provider.
const OTLPEndpoint = "http://" + collectorService + "." + PlaneNamespace + ":4318"

var tracingReadyMessage = fmt.Sprintf("traces are being collected and kept %d days; a Component sends them after its next new release", logRetentionDays)

// collectorConfig is Serendip's collector config.
//
//go:embed tracing-collector.yaml
var collectorConfig string

// collectorSelector selects the collector's pods (the chart's labels at
// 0.3.2).
var collectorSelector = map[string]string{
	"app.kubernetes.io/name":     "opentelemetry-collector",
	"app.kubernetes.io/instance": tracingRelease,
}

// tracingReleaseFor is the module's release. The config's hash goes in
// the pod annotations, so a changed config rolls the Deployment: the
// chart's own checksum covers only a ConfigMap it renders.
func tracingReleaseFor() release {
	return release{
		name: tracingRelease, chart: modCharts + "/observability-tracing-openobserve", version: "0.3.2",
		what: "the trace collector", noWait: true,
		sets: []string{
			// The logs module's store and its admin Secret are reused.
			"openobserve-standalone.enabled=false",
			"opentelemetry-collector.configMap.existingName=" + collectorConfigName,
			// It holds traces in memory and keeps about 10 spans a second
			// (T001); the memory limiter and the retention bound it.
			"opentelemetryCollectorCustomizations.tailSampling.enabled=false",
			"adapter.enabled=false",
		},
		strings: []string{"opentelemetry-collector.podAnnotations.serendip-collector-config=" + collectorConfigHash()},
	}
}

func collectorConfigHash() string {
	sum := sha256.Sum256([]byte(collectorConfig))
	return hex.EncodeToString(sum[:8])
}

func collectorConfigMap() kubernetes.Object {
	return kubernetes.Object{
		APIVersion: "v1", Kind: "ConfigMap",
		Metadata: kubernetes.ObjectMeta{Name: collectorConfigName, Namespace: PlaneNamespace, Labels: managedBy},
		Data:     mustJSON(map[string]string{collectorConfigKey: collectorConfig}),
	}
}

// TracingFingerprint is what a full install applies.
func TracingFingerprint() string {
	sum := sha256.Sum256([]byte(strings.Join(tracingReleaseFor().installArgs(), "\x00") + "\x00" + collectorConfig))
	return "tracing-" + hex.EncodeToString(sum[:8])
}

// TracingInstaller installs and removes the tracing package. It's a
// reconcile.PackageInstaller; core runs one package job at a time, so
// Install and Uninstall never run at once.
type TracingInstaller struct {
	run    helm.Runner
	client *kubernetes.Client
	// helm runs a release's install, recovering one left mid-operation,
	// as the observability package's do.
	helm *Installer
	now  func() time.Time
	// storeHTTP, when set, replaces the port-forward to the store (tests).
	storeHTTP *http.Client
	// deleteWait is how long Uninstall waits after deleting the traces
	// (streamDeleteWait; shorter in tests).
	deleteWait time.Duration

	mu sync.Mutex
	// lastInstall and repairs work as Installer's.
	lastInstall time.Time
	repairs     int
}

var _ reconcile.PackageInstaller = (*TracingInstaller)(nil)

// NewTracingInstaller builds a TracingInstaller, failing fast if `helm`
// isn't on $PATH (ADR 0010).
func NewTracingInstaller(client *kubernetes.Client, kubeconfigPath string) (*TracingInstaller, error) {
	if err := helm.CheckAvailable(); err != nil {
		return nil, fmt.Errorf("openchoreo tracing: %w", err)
	}
	return newTracingInstaller(helm.WithKubeconfig(helm.Run, kubeconfigPath), client), nil
}

func newTracingInstaller(run helm.Runner, client *kubernetes.Client) *TracingInstaller {
	return &TracingInstaller{run: run, client: client, helm: newInstaller(run, client), now: time.Now, deleteWait: streamDeleteWait}
}

// Install brings the package towards installed. It needs the
// observability package's store, and installs nothing until that's
// ready. With a matching fingerprint and a ready collector it applies
// nothing; a collector not ready soon after an install is waited out,
// then the install is run again, each time waiting twice as long.
func (i *TracingInstaller) Install(ctx context.Context, inst resource.ProviderInstance) (reconcile.PackageState, error) {
	if msg, err := i.prerequisite(ctx); msg != "" || err != nil {
		return reconcile.PackageState{Message: msg}, err
	}
	want := TracingFingerprint()
	if inst.Status.Applied != want {
		return i.install(ctx, want)
	}
	i.mu.Lock()
	if i.lastInstall.IsZero() {
		i.lastInstall = i.now()
	}
	i.mu.Unlock()
	msg, restart, err := i.check(ctx)
	if err != nil {
		return reconcile.PackageState{}, err
	}
	if msg == "" {
		i.reset()
		return reconcile.PackageState{Ready: true, Message: tracingReadyMessage, Applied: want}, nil
	}
	if restart {
		// The config was changed or deleted: re-applied, and the pods
		// restarted, since the check can't see what a running pod loaded.
		if err := i.applyConfig(ctx, true); err != nil {
			return reconcile.PackageState{}, err
		}
		// The new pods' start is waited out as an install's.
		i.mu.Lock()
		i.lastInstall = i.now()
		i.mu.Unlock()
		return reconcile.PackageState{Message: msg, Applied: want}, nil
	}
	if i.waitOut() {
		return reconcile.PackageState{Message: msg, Applied: want}, nil
	}
	return i.install(ctx, want)
}

// prerequisite says why the package can't install yet, or "" when the
// observability package's store and its credentials are there.
func (i *TracingInstaller) prerequisite(ctx context.Context) (string, error) {
	const need = "waiting for the observability package: traces are kept in its log store, so it must be installed and Ready first"
	pods, err := i.client.List(ctx, planePodsPath)
	if err != nil {
		return "", fmt.Errorf("openchoreo tracing: list pods: %w", err)
	}
	if !anyReady(pods, logsStoreSelector) {
		return need, nil
	}
	if _, err := i.client.Get(ctx, secretsPath+"/"+adminSecretName); errors.Is(err, kubernetes.ErrNotFound) {
		return need, nil
	} else if err != nil {
		return "", fmt.Errorf("openchoreo tracing: get %s secret: %w", adminSecretName, err)
	}
	return "", nil
}

// install applies the config, then the module, without Helm's wait. It
// waits for an earlier removal's deletion of the traces to finish first:
// spans written meanwhile would be deleted with the old ones.
func (i *TracingInstaller) install(ctx context.Context, want string) (reconcile.PackageState, error) {
	if deleting, err := i.streamDeleting(ctx); err != nil {
		return reconcile.PackageState{}, err
	} else if deleting {
		return reconcile.PackageState{Message: "waiting for the traces of an earlier install to finish being deleted; the store does it within a minute"}, nil
	}
	if err := i.applyConfig(ctx, false); err != nil {
		return reconcile.PackageState{}, err
	}
	r := tracingReleaseFor()
	if err := i.helm.helmInstall(ctx, r); err != nil {
		return reconcile.PackageState{}, fmt.Errorf("openchoreo tracing: install %s: %w", r.what, err)
	}
	i.mu.Lock()
	i.lastInstall = i.now()
	i.mu.Unlock()
	msg, _, err := i.check(ctx)
	if err != nil {
		return reconcile.PackageState{}, err
	}
	if msg != "" {
		return reconcile.PackageState{Message: msg, Applied: want}, nil
	}
	i.reset()
	return reconcile.PackageState{Ready: true, Message: tracingReadyMessage, Applied: want}, nil
}

func (i *TracingInstaller) applyConfig(ctx context.Context, restart bool) error {
	if _, err := apply(ctx, i.client, configMapsPath, collectorConfigMap()); err != nil {
		return fmt.Errorf("openchoreo tracing: %w", err)
	}
	if !restart {
		return nil
	}
	pods, err := i.client.List(ctx, planePodsPath)
	if err != nil {
		return fmt.Errorf("openchoreo tracing: list pods: %w", err)
	}
	for _, p := range pods {
		if matches(p.Metadata.Labels, collectorSelector) {
			if err := deleteIfExists(ctx, i.client, planePodsPath+"/"+p.Metadata.Name); err != nil {
				return fmt.Errorf("openchoreo tracing: restart the trace collector: %w", err)
			}
		}
	}
	return nil
}

// check reports what's wrong, or "": the config is Serendip's, and a
// collector pod is ready. restart: the config must be re-applied and the
// collector restarted.
func (i *TracingInstaller) check(ctx context.Context) (msg string, restart bool, err error) {
	cm, err := i.client.Get(ctx, configMapsPath+"/"+collectorConfigName)
	if errors.Is(err, kubernetes.ErrNotFound) {
		return "the trace collector's configuration is missing", true, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("openchoreo tracing: get configmap %s: %w", collectorConfigName, err)
	}
	if !dataEqual(cm.Data, collectorConfigMap().Data) {
		return "the trace collector's configuration was changed", true, nil
	}
	pods, err := i.client.List(ctx, planePodsPath)
	if err != nil {
		return "", false, fmt.Errorf("openchoreo tracing: list pods: %w", err)
	}
	if !anyReady(pods, collectorSelector) {
		return "waiting for the trace collector to be ready", false, nil
	}
	return "", false, nil
}

func (i *TracingInstaller) reset() {
	i.mu.Lock()
	i.repairs = 0
	i.mu.Unlock()
}

// waitOut works as Installer.waitOut.
func (i *TracingInstaller) waitOut() bool {
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

// streamDeleting reports whether the traces stream's deletion is queued.
func (i *TracingInstaller) streamDeleting(ctx context.Context) (bool, error) {
	now := i.now()
	var hits []json.RawMessage
	err := searchStore(ctx, i.client, i.storeHTTP, "traces", "traces", `SELECT count(*) AS n FROM "`+tracesStream+`"`,
		now.Add(-time.Minute), now, 1, &hits)
	switch {
	case errors.Is(err, errStreamDeleting):
		return true, nil
	case err == nil, errors.Is(err, errNoStream):
		return false, nil
	}
	return false, fmt.Errorf("openchoreo tracing: %w", err)
}

// Uninstall removes the module and its config, then deletes the traces
// stream (every stored trace; the package's Removal says so). Every step
// treats "already gone" as done, and a store that's gone (the
// observability package removed first) took the traces with it.
func (i *TracingInstaller) Uninstall(ctx context.Context, instanceID string) error {
	if _, err := i.run(ctx, "uninstall", tracingRelease, "--namespace", PlaneNamespace,
		"--ignore-not-found", "--wait", "--timeout", helmUninstallTimeout); err != nil {
		return fmt.Errorf("openchoreo tracing: uninstall the trace collector: %w", err)
	}
	if err := deleteIfExists(ctx, i.client, configMapsPath+"/"+collectorConfigName); err != nil {
		return fmt.Errorf("openchoreo tracing: %w", err)
	}
	return i.deleteStream(ctx)
}

// streamDeleteWait is how long a stream's deletion may take to run: the
// store queues it for its retention job, which the observability package
// runs every minute (values.go). Until it has run, spans written to the
// stream are deleted with it (T007, live: 22 s after the request), and
// nothing the store answers says whether it has.
const streamDeleteWait = 90 * time.Second

// deleteStream deletes the traces stream (only the traces type, so the
// logs stream of the same name stays), then waits out streamDeleteWait,
// so a new install can't write spans the deletion would take. It runs in
// the package job's goroutine, not the reconcile loop.
func (i *TracingInstaller) deleteStream(ctx context.Context) error {
	pods, err := i.client.List(ctx, planePodsPath)
	if err != nil {
		return fmt.Errorf("openchoreo tracing: list pods: %w", err)
	}
	_, err = i.client.Get(ctx, secretsPath+"/"+adminSecretName)
	switch {
	case errors.Is(err, kubernetes.ErrNotFound) && !anyReady(pods, logsStoreSelector):
		return nil // the store is gone, and the traces with it
	case err != nil && !errors.Is(err, kubernetes.ErrNotFound):
		return fmt.Errorf("openchoreo tracing: get %s secret: %w", adminSecretName, err)
	}
	auth, err := logsStoreAuth(ctx, i.client)
	if err != nil {
		return fmt.Errorf("openchoreo tracing: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete,
		fmt.Sprintf("http://%s.%s:%d/api/%s/streams/%s?type=traces", logsStoreService, PlaneNamespace, logsStorePort, logsStoreOrg, url.PathEscape(tracesStream)), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", auth.Get("Authorization"))
	hc := i.storeHTTP
	if hc == nil {
		var done func()
		hc, done = storeHTTPClient(i.client)
		defer done()
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("openchoreo tracing: delete the stored traces: %w", err)
	}
	resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNotFound:
		// Nothing stored, or deleted by an earlier run (RP-086: a restart
		// inside the wait doesn't wait again).
		return nil
	case http.StatusOK:
	default:
		return fmt.Errorf("openchoreo tracing: delete the stored traces: status %d", resp.StatusCode)
	}
	select {
	case <-ctx.Done():
		return fmt.Errorf("openchoreo tracing: waiting for the stored traces' deletion: %w", ctx.Err())
	case <-time.After(i.deleteWait):
	}
	return nil
}
