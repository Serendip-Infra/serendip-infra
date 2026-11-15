package main

import (
	"context"
	"database/sql"
	"log"

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
	"and the built-in disk rule goes with the package. Notification channels stay."

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
// plan decision 5). builds installs the workflow plane (spec 022) and
// observability the observability plane (spec 023); either is nil when
// there's no cluster to install it on. The catalogue refuses any package
// without measured Requirements: builds' were measured in spec 022's
// T013, and observability's in spec 023's T012.
func packageCatalogue(builds, observability reconcile.PackageInstaller) reconcile.Catalogue {
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
	}
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
