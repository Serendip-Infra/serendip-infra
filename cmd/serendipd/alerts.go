package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/netip"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/serendip-infra/serendip-infra/core/event"
	"github.com/serendip-infra/serendip-infra/core/graph"
	"github.com/serendip-infra/serendip-infra/core/notify"
	"github.com/serendip-infra/serendip-infra/core/reconcile"
	"github.com/serendip-infra/serendip-infra/core/resource"
	"github.com/serendip-infra/serendip-infra/core/store"
	"github.com/serendip-infra/serendip-infra/core/store/sqlite"
)

// alerting is the alert evaluator and its delivery loop (spec 024 T005),
// and the sender they share with the heartbeat and test sends.
type alerting struct {
	evaluator *reconcile.AlertEvaluator
	delivery  *reconcile.DeliveryLoop
	client    *notify.Client
	// ranges reads the cluster's own ranges for the sender's address
	// check; nil reads none.
	ranges func(context.Context) ([]netip.Prefix, error)
}

// wireAlerting builds the evaluator over the observability capability and
// the delivery loop over core/notify. secrets is nil without a cluster;
// a send then fails, retryably, until one is configured. ranges reads the
// cluster's own address ranges, which no notification may reach.
func wireAlerting(db *sql.DB, bus event.Bus, obs reconcile.ObservabilityProvider, secrets *reconcile.SecretReconciler, consoleURL string,
	ranges func(context.Context) ([]netip.Prefix, error),
) *alerting {
	stores := reconcile.AlertStores{
		Rules:        sqlite.NewStore[resource.AlertRuleSpec, resource.AlertRuleStatus](db, resource.KindAlertRule).SetBus(bus),
		Alerts:       sqlite.NewStore[resource.AlertSpec, resource.AlertStatus](db, resource.KindAlert).SetBus(bus),
		Channels:     sqlite.NewStore[resource.NotificationChannelSpec, resource.NotificationChannelStatus](db, resource.KindNotificationChannel).SetBus(bus),
		SMTP:         sqlite.NewStore[resource.SMTPServerSpec, resource.SMTPServerStatus](db, resource.KindSMTPServer).SetBus(bus),
		Deployments:  sqlite.NewStore[resource.DeploymentSpec, resource.DeploymentStatus](db, resource.KindDeployment).SetBus(bus),
		Components:   sqlite.NewStore[resource.ComponentSpec, resource.ComponentStatus](db, resource.KindComponent).SetBus(bus),
		Environments: sqlite.NewStore[resource.EnvironmentSpec, resource.EnvironmentStatus](db, resource.KindEnvironment).SetBus(bus),
		Applications: sqlite.NewStore[resource.ApplicationSpec, resource.ApplicationStatus](db, resource.KindApplication).SetBus(bus),
		Projects:     sqlite.NewStore[resource.ProjectSpec, resource.ProjectStatus](db, resource.KindProject).SetBus(bus),
		Owners:       graph.NewSQLiteGraph(db),
	}
	instances := sqlite.NewStore[resource.ProviderInstanceSpec, resource.ProviderInstanceStatus](db, resource.KindProviderInstance)
	client := notify.NewClient(notify.Options{})
	e, d := reconcile.NewAlertEvaluator(reconcile.AlertEvaluatorOptions{
		Stores:   stores,
		Provider: obs,
		Installed: func(ctx context.Context) (bool, error) {
			list, err := instances.List(ctx, store.ListOptions{})
			if err != nil {
				return false, err
			}
			for _, pi := range list {
				if pi.Spec.CapabilityID == capObservability {
					return true, nil
				}
			}
			return false, nil
		},
		Purge: func(ctx context.Context, cutoff time.Time, limit int) (int, error) {
			return sqlite.PurgeResolvedAlerts(ctx, db, bus, cutoff, limit)
		},
	}, reconcile.DeliveryOptions{
		Notifier:   client,
		Secrets:    secretValue(secrets),
		ConsoleURL: consoleURL,
		Source:     "Serendip",
	})
	return &alerting{evaluator: e, delivery: d, client: client, ranges: ranges}
}

// run starts both loops. They stop with ctx; the returned channel closes
// once both have, so the store isn't closed under a write.
func (a *alerting) run(ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); a.evaluator.Run(ctx) }()
	go func() { defer wg.Done(); a.delivery.Run(ctx) }()
	go func() { wg.Wait(); close(done) }()
	// The ranges loop touches only the cluster, never the store, so
	// shutdown doesn't wait for it.
	if a.ranges != nil {
		go refreshClusterRanges(ctx, a.ranges, a.client.Check)
	}
	return done
}

// How often the cluster's own ranges are re-read: nodes join and leave,
// so the sender's check follows them (spec 024 T006). A failed read is
// retried sooner. Variables only so a test can shorten them.
var (
	clusterRangesEvery = 5 * time.Minute
	clusterRangesRetry = 30 * time.Second
)

// refreshClusterRanges keeps check's cluster ranges current until ctx
// ends. A full read replaces them. A partly failed one adds what it found
// to the last ranges, so a failed Service-range read doesn't drop the
// Service ranges known before; an empty answer changes nothing.
func refreshClusterRanges(ctx context.Context, read func(context.Context) ([]netip.Prefix, error), check *notify.Checker) {
	logged := ""
	var last []netip.Prefix
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		rctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		ranges, err := read(rctx)
		cancel()
		if err != nil {
			ranges = mergeRanges(last, ranges)
		}
		if len(ranges) > 0 {
			check.SetClusterRanges(ranges)
			last = ranges
		}
		wait := clusterRangesEvery
		if err != nil {
			wait = clusterRangesRetry
			if msg := err.Error(); msg != logged && ctx.Err() == nil {
				log.Printf("serendipd: alerts: the cluster's address ranges: %v", err)
				logged = msg
			}
		} else {
			logged = ""
		}
		timer.Reset(wait)
	}
}

// mergeRanges is a and b without repeats, in order.
func mergeRanges(a, b []netip.Prefix) []netip.Prefix {
	seen := make(map[netip.Prefix]bool, len(a)+len(b))
	out := make([]netip.Prefix, 0, len(a)+len(b))
	for _, p := range append(slices.Clone(a), b...) {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

// secretValue reads a channel's Secret as one string: its "value" key, or
// its only key.
func secretValue(secrets *reconcile.SecretReconciler) notify.SecretReader {
	return func(ctx context.Context, id string) (string, error) {
		if secrets == nil {
			return "", reconcile.ErrProviderNotReady
		}
		data, err := secrets.ReadValue(ctx, id)
		if err != nil {
			return "", err
		}
		return oneSecretString(data)
	}
}

func oneSecretString(data map[string]any) (string, error) {
	if v, ok := data["value"]; ok {
		s, ok := v.(string)
		if !ok {
			return "", errors.New(`the Secret's "value" isn't a string`)
		}
		return s, nil
	}
	if len(data) != 1 {
		keys := make([]string, 0, len(data))
		for k := range data {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return "", fmt.Errorf(`the Secret has keys %v: want one key, or "value"`, keys)
	}
	for _, v := range data {
		if s, ok := v.(string); ok {
			return s, nil
		}
	}
	return "", errors.New("the Secret's value isn't a string")
}
