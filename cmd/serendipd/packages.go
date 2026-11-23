package main

import (
	"context"
	"database/sql"
	"log"
	"sync/atomic"

	"github.com/serendip-infra/serendip-infra/core/event"
	"github.com/serendip-infra/serendip-infra/core/provider"
	"github.com/serendip-infra/serendip-infra/core/reconcile"
	"github.com/serendip-infra/serendip-infra/core/resource"
	"github.com/serendip-infra/serendip-infra/core/store"
	"github.com/serendip-infra/serendip-infra/core/store/sqlite"
	buildplane "github.com/serendip-infra/serendip-infra/providers/build/openchoreo"
	obsplane "github.com/serendip-infra/serendip-infra/providers/observability/openchoreo"
)

const (
	capBuild         = "build"
	capObservability = "observability"
	capTracing       = "tracing"
)

// buildsRemoval is what removing the builds package loses (specs/022-builds
// plan decision 12), shown before the request to remove it.
const buildsRemoval = "Build history stays, but run logs and the stored images built from source are deleted. " +
	"Components running a built image keep running, but can't be rebuilt, or started on a node that doesn't have the image, until the package is back."

// observabilityRemoval is what removing the observability package loses
// (specs/023-observability plan decision 13, and specs/024-alerts spec
// IN 13 for alerting), shown before the request to
// remove it.
const observabilityRemoval = "All stored logs and metrics history are deleted. Recent logs and events on the Runtime tab keep working. " +
	"Finished builds' logs can no longer be read once their pods are gone. " +
	"Alert rules stop being evaluated and show as inactive, open Alerts are resolved as \"package removed\", " +
	"and the built-in disk rule goes with the package. Notification channels stay. " +
	"If the tracing package is installed, its trace collector is removed too and its traces are deleted with the logs; " +
	"it's set up again once this package is back."

// tracingRemoval is what removing the tracing package loses
// (specs/025-networking plan decision 10, RP-086).
const tracingRemoval = "All stored traces are deleted, which takes about a minute and a half. Logs and metrics stay. " +
	"Each Component sending traces restarts once, without the trace settings."

// observabilityDiskRule is the observability package's built-in rule
// (specs/024-alerts plan decision 4): stored data at 85% of its disk
// budget, sent to the platform's default channels.
var observabilityDiskRule = reconcile.BuiltInAlertRule{
	Name: "observability-disk",
	Spec: resource.AlertRuleSpec{
		Condition: resource.AlertCondition{Type: resource.ConditionTypePackageDisk, PackageDisk: &resource.PackageDiskCondition{PercentOfBudget: 85}},
		Severity:  resource.SeverityWarning,
		Summary:   "Stored logs and metrics are near the observability package's disk budget",
	},
}

// packageCatalogue is the optional packages this build offers (specs/020
// plan decision 5). builds installs the workflow plane (spec 022),
// observability the observability plane (spec 023) and tracing the trace
// collector (spec 025); each is nil when there's no cluster to install it
// on. The catalogue refuses any package without measured Requirements:
// builds' were measured in spec 022's T013, and observability's in spec
// 023's T012. Tracing's are measured in spec 025's T013, so until then it
// can't be installed.
func packageCatalogue(builds, observability, tracing reconcile.PackageInstaller) reconcile.Catalogue {
	return reconcile.Catalogue{
		"openchoreo-workflow-plane": {
			Provider: "openchoreo-workflow-plane", Capability: capBuild,
			Description:  "Builds container images from source in the cluster",
			Installer:    builds,
			Requirements: buildplane.Requirements,
			Removal:      buildsRemoval,
		},
		"openchoreo-observability-plane": {
			Provider: "openchoreo-observability-plane", Capability: capObservability,
			Description:  "Stored logs and metrics history; heavy on a 16 GB machine",
			Installer:    observability,
			Requirements: obsplane.Requirements,
			Removal:      observabilityRemoval,
			AlertRules:   []reconcile.BuiltInAlertRule{observabilityDiskRule},
		},
		"openchoreo-tracing": {
			Provider: "openchoreo-tracing", Capability: capTracing,
			Description: "Traces Components send, kept in the observability package's store; needs that package",
			Installer:   tracing,
			// Measured in spec 025's T013.
			Requirements: nil,
			Removal:      tracingRemoval,
		},
	}
}

// observabilityPackage is the observability package's installer, with
// what cmd/serendipd adds around it (spec 025 T008):
//   - an install or check waits for the network layer's flow source, so
//     the log collector isn't applied without it and again with it;
//   - a removal removes the trace collector first, and holds the tracing
//     package's installs off until this package is installed again: the
//     collector writes to this package's store.
type observabilityPackage struct {
	*obsplane.Installer
	flowSource func(context.Context) error
	// removing is shared with tracingPackage.
	removing *atomic.Bool
	// removeTracing removes the trace collector; nil without one.
	removeTracing func(ctx context.Context, instanceID string) error
}

func (p *observabilityPackage) Install(ctx context.Context, inst resource.ProviderInstance) (reconcile.PackageState, error) {
	if err := p.flowSource(ctx); err != nil {
		return reconcile.PackageState{}, err
	}
	st, err := p.Installer.Install(ctx, inst)
	if err == nil {
		// Installed again, or on its way: tracing may go ahead, since its
		// own prerequisite waits for the store. One package's install and
		// removal never run at once (core's package jobs). The flag is in
		// memory: after a restart mid-removal it's set again only when the
		// removal is retried.
		p.removing.Store(false)
	}
	return st, err
}

func (p *observabilityPackage) Uninstall(ctx context.Context, instanceID string) error {
	p.removing.Store(true)
	if p.removeTracing != nil {
		// The tracing package's own instance (if any) stays, so there's
		// no ID of its to give; its installer doesn't use one.
		if err := p.removeTracing(ctx, ""); err != nil {
			return err
		}
	}
	return p.Installer.Uninstall(ctx, instanceID)
}

// tracingPackage is the tracing package's installer, with the trace
// endpoint the application provider gives workloads set while its
// ProviderInstance exists (plan decision 11 as amended in T007): from its
// first install or check, and cleared when it's removed. Not while it
// reads Ready, so a settling collector doesn't roll every traced Component
// twice.
type tracingPackage struct {
	*obsplane.TracingInstaller
	setEndpoint func(string)
	// obsRemoving is set while the observability package is being
	// removed.
	obsRemoving *atomic.Bool
}

func (p *tracingPackage) Install(ctx context.Context, inst resource.ProviderInstance) (reconcile.PackageState, error) {
	p.setEndpoint(obsplane.OTLPEndpoint)
	if p.obsRemoving.Load() {
		return reconcile.PackageState{Message: "waiting for the observability package: it's being removed, and traces are kept in its store"}, nil
	}
	return p.TracingInstaller.Install(ctx, inst)
}

func (p *tracingPackage) Uninstall(ctx context.Context, instanceID string) error {
	if err := p.TracingInstaller.Uninstall(ctx, instanceID); err != nil {
		return err
	}
	p.setEndpoint("")
	return nil
}

// onReadiness is called with each capability's readiness as packages
// report it, to hand a package's provider to the handles that use it
// while it's Ready.
type onReadiness map[string]func(ready bool)

// registerPackages wires the ProviderInstance reconciler into engine,
// reports each optional capability not installed until it's reconciled,
// and returns the clean-up that uninstalls a removed package.
func registerPackages(db *sql.DB, bus event.Bus, engine *reconcile.Engine, registry *provider.Registry, c reconcile.Catalogue, handles onReadiness) reconcile.CleanupFunc {
	report := func(capability string, ready bool, reason, message string) {
		registry.SetReadiness(capability, provider.Readiness{Ready: ready, Reason: reason, Message: message})
		if h := handles[capability]; h != nil {
			h(ready)
		}
	}
	instances := sqlite.NewStore[resource.ProviderInstanceSpec, resource.ProviderInstanceStatus](db, resource.KindProviderInstance).SetBus(bus)
	rules := sqlite.NewStore[resource.AlertRuleSpec, resource.AlertRuleStatus](db, resource.KindAlertRule).SetBus(bus)
	r := reconcile.NewProviderInstanceReconciler(instances, c, report).WithAlertRules(rules)
	if err := r.ReportStart(context.Background()); err != nil {
		log.Printf("serendipd: packages: report readiness at start: %v", err)
	}
	engine.Register(resource.KindProviderInstance, r, lister(instances, resource.KindProviderInstance))
	return r.UninstallCleanup()
}

// packageHandle sets l to p while capability's package is Ready, and
// clears it otherwise, registering p each time it becomes Ready. name is
// the provider, for the log. The get-then-set isn't atomic: it relies on
// the reconciler reporting one package's readiness at a time.
func packageHandle[T any](registry *provider.Registry, capability, name string, l *lazy[T], p T) func(ready bool) {
	return func(ready bool) {
		_, err := l.get()
		active := err == nil
		switch {
		case !ready && active:
			l.clear()
			log.Printf("serendipd: kubernetes: %s provider (%s) not ready", capability, name)
		case ready && !active:
			registry.Register(capability, p)
			l.set(p)
			log.Printf("serendipd: kubernetes: %s provider (%s) active", capability, name)
		}
	}
}

// tracingHandle is the tracing package's handle (spec 025).
func tracingHandle(registry *provider.Registry, tr *lazyTracing, p reconcile.TracingProvider) func(ready bool) {
	return packageHandle(registry, capTracing, "openchoreo tracing", &tr.lazy, p)
}

// buildHandle is the build package's handle (spec 022).
func buildHandle(registry *provider.Registry, builds *lazyBuilds, p reconcile.BuildProvider) func(ready bool) {
	return packageHandle(registry, capBuild, "openchoreo workflow plane", &builds.lazy, p)
}

// observabilityHandle is the observability package's handle (spec 023).
func observabilityHandle(registry *provider.Registry, obs *lazyObservability, p reconcile.ObservabilityProvider) func(ready bool) {
	return packageHandle(registry, capObservability, "openchoreo observability plane", &obs.lazy, p)
}

// registerBuilds wires the Build reconciler (spec 022) against the lazy
// build handle, with credentials read through the Secrets capability, and
// lets the Component reconciler run source Components. Must run before
// engine.Run.
func registerBuilds(db *sql.DB, bus event.Bus, engine *reconcile.Engine,
	comps *reconcile.ComponentReconciler,
	components store.Store[resource.ComponentSpec, resource.ComponentStatus],
	apps store.Store[resource.ApplicationSpec, resource.ApplicationStatus],
	builds reconcile.BuildProvider, creds reconcile.CredentialReader,
) {
	buildStore := sqlite.NewStore[resource.BuildSpec, resource.BuildStatus](db, resource.KindBuild).SetBus(bus)
	comps.WithBuilds(buildStore, builds, creds)
	engine.Register(resource.KindBuild, reconcile.NewBuildReconciler(buildStore, components, apps, builds, creds, engine.Enqueue), lister(buildStore, resource.KindBuild))
}
