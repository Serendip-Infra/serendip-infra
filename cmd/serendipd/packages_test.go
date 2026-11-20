package main

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/serendip-infra/serendip-infra/core/event"
	"github.com/serendip-infra/serendip-infra/core/provider"
	"github.com/serendip-infra/serendip-infra/core/reconcile"
	"github.com/serendip-infra/serendip-infra/core/resource"
	"github.com/serendip-infra/serendip-infra/core/store/sqlite"
	buildplane "github.com/serendip-infra/serendip-infra/providers/build/openchoreo"
	obsplane "github.com/serendip-infra/serendip-infra/providers/observability/openchoreo"
)

// The optional capabilities show in GET /v1/readiness from the start, as
// not available in a build without their installers.
func TestPackagesReportReadiness(t *testing.T) {
	db, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := sqlite.Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	bus := event.NewInProcessBus()
	registry := provider.NewRegistry()
	clean := registerPackages(db, bus, reconcile.NewEngine(bus, time.Minute, time.Minute), registry, packageCatalogue(nil, nil), nil)
	if clean == nil {
		t.Fatal("no clean-up")
	}
	got := registry.Readiness()
	for _, capability := range []string{"build", "observability"} {
		if rd, ok := got[capability]; !ok || rd.Ready || rd.Reason != reconcile.ReasonUnavailable {
			t.Errorf("%s readiness = %+v", capability, rd)
		}
	}
}

// The builds package carries its measured cost (T013), so it can be
// installed, and the catalogue states the same numbers the package does.
func TestBuildsPackageInstallableWithMeasuredCost(t *testing.T) {
	c := packageCatalogue(installerStub{}, nil)
	p := c["openchoreo-workflow-plane"]
	if p.Installer == nil || p.Removal == "" || p.Capability != capBuild {
		t.Fatalf("builds entry = %+v", p)
	}
	if err := p.Installable(); err != nil {
		t.Fatalf("Installable() = %v, want nil", err)
	}
	if p.Requirements == nil || *p.Requirements != *buildplane.Requirements {
		t.Fatalf("Requirements = %+v, want the package's %+v", p.Requirements, buildplane.Requirements)
	}
	if err := c.Validate(resource.ProviderInstanceSpec{ProviderID: p.Provider, CapabilityID: capBuild}); err != nil {
		t.Fatalf("Validate = %v", err)
	}
}

// The build handle answers ErrProviderNotReady until the package is
// Ready, and again once it stops being Ready.
func TestBuildHandleFollowsReadiness(t *testing.T) {
	registry := provider.NewRegistry()
	builds := &lazyBuilds{}
	var p reconcile.BuildProvider = &lazyBuilds{} // any provider will do
	set := buildHandle(registry, builds, p)
	notReady := func() bool {
		_, err := builds.HeadCommit(t.Context(), resource.ComponentSource{}, nil)
		return errors.Is(err, reconcile.ErrProviderNotReady)
	}
	if !notReady() {
		t.Fatal("handle ready before the package")
	}
	set(true)
	if got, err := builds.get(); err != nil || got != p {
		t.Fatalf("after Ready: %v, %v", got, err)
	}
	if impl, ok := registry.Lookup(capBuild); !ok || impl != p {
		t.Fatalf("registry build = %v, %v", impl, ok)
	}
	set(true) // a repeated report changes nothing
	set(false)
	if !notReady() {
		t.Fatal("handle still set after the package stopped being Ready")
	}
}

// The observability package carries its measured cost (T012), including
// its disk use, so it can be installed, and the catalogue states the same
// numbers the package does.
func TestObservabilityPackageInstallableWithMeasuredCost(t *testing.T) {
	c := packageCatalogue(nil, installerStub{})
	p := c["openchoreo-observability-plane"]
	if p.Installer == nil || p.Capability != capObservability || p.Removal != observabilityRemoval {
		t.Fatalf("observability entry = %+v", p)
	}
	if err := p.Installable(); err != nil {
		t.Fatalf("Installable() = %v, want nil", err)
	}
	if p.Requirements != obsplane.Requirements || p.Requirements.Disk == nil {
		t.Fatalf("Requirements = %+v, want the package's %+v with Disk", p.Requirements, obsplane.Requirements)
	}
	if err := c.Validate(resource.ProviderInstanceSpec{ProviderID: p.Provider, CapabilityID: capObservability}); err != nil {
		t.Fatalf("Validate = %v", err)
	}
}

// With its measured requirements, an observability install runs through the
// engine to Ready, and the handle then reaches the provider.
func TestObservabilityInstallReachesHandle(t *testing.T) {
	db, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := sqlite.Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	bus := event.NewInProcessBus()
	registry := provider.NewRegistry()
	engine := reconcile.NewEngine(bus, time.Minute, time.Minute)
	c := packageCatalogue(nil, readyInstaller{})
	obs := &lazyObservability{}
	want := &observabilityStub{}
	registerPackages(db, bus, engine, registry, c, onReadiness{capObservability: observabilityHandle(registry, obs, want)})
	if rd := registry.Readiness()[capObservability]; rd.Ready || rd.Reason != reconcile.ReasonNotInstalled {
		t.Fatalf("before install: %+v", rd)
	}
	if _, err := obs.Metrics(t.Context(), reconcile.LogTarget{}, reconcile.MetricsQuery{}); !errors.Is(err, reconcile.ErrProviderNotReady) {
		t.Fatalf("handle before install: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	var runErr error
	go func() { defer close(done); runErr = engine.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	select {
	case <-engine.Ready():
	case <-done:
		t.Fatalf("engine stopped before it was ready: %v", runErr)
	case <-time.After(10 * time.Second):
		t.Fatal("engine not ready")
	}
	insts := sqlite.NewStore[resource.ProviderInstanceSpec, resource.ProviderInstanceStatus](db, resource.KindProviderInstance).SetBus(bus)
	if _, err := insts.Create(t.Context(), resource.ProviderInstance{Metadata: resource.Metadata{Name: "observability"},
		Spec: resource.ProviderInstanceSpec{ProviderID: "openchoreo-observability-plane", CapabilityID: capObservability}}); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := obs.Metrics(t.Context(), reconcile.LogTarget{}, reconcile.MetricsQuery{}); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("handle not ready; readiness %+v", registry.Readiness()[capObservability])
		}
		time.Sleep(20 * time.Millisecond)
	}
	if want.metrics.Load() != 1 {
		t.Errorf("provider saw %d metrics reads, want 1", want.metrics.Load())
	}
	if rd := registry.Readiness()[capObservability]; !rd.Ready {
		t.Errorf("readiness = %+v", rd)
	}
	if impl, ok := registry.Lookup(capObservability); !ok || impl != reconcile.ObservabilityProvider(want) {
		t.Errorf("registry observability = %v, %v", impl, ok)
	}
}

// The observability handle follows readiness like the build handle.
func TestObservabilityHandleFollowsReadiness(t *testing.T) {
	registry := provider.NewRegistry()
	obs := &lazyObservability{}
	set := observabilityHandle(registry, obs, &observabilityStub{})
	notReady := func() bool {
		_, err := obs.SearchLogs(t.Context(), nil, reconcile.LogQuery{})
		return errors.Is(err, reconcile.ErrProviderNotReady)
	}
	if !notReady() {
		t.Fatal("handle ready before the package")
	}
	set(true)
	if notReady() {
		t.Fatal("handle not set once Ready")
	}
	set(false)
	if !notReady() {
		t.Fatal("handle still set after the package stopped being Ready")
	}
	set(true) // Ready again, e.g. after a repair
	if notReady() {
		t.Fatal("handle not set when Ready again")
	}
}

type observabilityStub struct{ metrics atomic.Int32 }

func (*observabilityStub) SearchLogs(context.Context, []reconcile.LogTarget, reconcile.LogQuery) (reconcile.LogPage, error) {
	return reconcile.LogPage{}, nil
}
func (*observabilityStub) BuildLogs(context.Context, resource.Build, reconcile.LogQuery) (reconcile.LogPage, error) {
	return reconcile.LogPage{}, nil
}

func (*observabilityStub) EvaluateCondition(context.Context, reconcile.LogTarget, resource.AlertCondition, time.Duration) (reconcile.ConditionValue, error) {
	return reconcile.ConditionValue{}, reconcile.ErrNoData
}

func (o *observabilityStub) Metrics(context.Context, reconcile.LogTarget, reconcile.MetricsQuery) (reconcile.Metrics, error) {
	o.metrics.Add(1)
	return reconcile.Metrics{}, nil
}

type readyInstaller struct{ installerStub }

func (readyInstaller) Install(context.Context, resource.ProviderInstance) (reconcile.PackageState, error) {
	return reconcile.PackageState{Ready: true, Applied: "test"}, nil
}

type installerStub struct{}

func (installerStub) Install(context.Context, resource.ProviderInstance) (reconcile.PackageState, error) {
	return reconcile.PackageState{}, nil
}
func (installerStub) Uninstall(context.Context, string) error { return nil }
