package openchoreo

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/serendip-infra/serendip-infra/core/reconcile"
	"github.com/serendip-infra/serendip-infra/providers/kubernetes"
)

// The package's current disk use (plan decision 8, as amended by the
// finding from T002): the kubelet doesn't measure local-path volumes, so
// each store's own /metrics is read through the Service proxy instead,
// with no credentials:
//   - OpenObserve: storage_compressed_bytes, per stream, summed. The
//     module turns its /metrics off; the installer turns it on.
//   - Prometheus: its blocks and its WAL.
//
// Both are reported in bytes. T012 checks the sum against a du of the
// volumes (VERSIONS.md).

const (
	logsStoreService = "openobserve"

	prometheusService = "openchoreo-observability-prometheus"
	prometheusPort    = 9091

	// usageEvery bounds how often the stores are read (decision 8).
	usageEvery = 30 * time.Second
)

var (
	logsStoreMetrics  = []string{"storage_compressed_bytes"}
	metricsStoreUsage = []string{"prometheus_tsdb_storage_blocks_bytes", "prometheus_tsdb_wal_storage_size_bytes"}
)

// capacityMiB is the stores' volumes: logs 5 Gi, metrics 3 Gi
// (values.go, prometheus-values.yaml).
const capacityMiB = 5<<10 + 3<<10

var _ reconcile.PackageUsage = (*Installer)(nil)

// Usage reads the package's current disk use, at most once per
// usageEvery; between reads it answers the last one, or the last read's
// error.
func (i *Installer) Usage(ctx context.Context) (reconcile.DiskUsage, error) {
	now := i.now()
	i.mu.Lock()
	last, lastErr, errAt := i.usage, i.usageErr, i.usageErrAt
	i.mu.Unlock()
	if lastErr != nil && now.Sub(errAt) < usageEvery {
		return reconcile.DiskUsage{}, lastErr
	}
	if lastErr == nil && !last.ReadAt.IsZero() && now.Sub(last.ReadAt) < usageEvery {
		return last, nil
	}
	u, err := i.readUsage(ctx, now)
	i.mu.Lock()
	if err != nil {
		i.usageErr, i.usageErrAt = err, now
	} else {
		i.usage, i.usageErr = u, nil
	}
	i.mu.Unlock()
	return u, err
}

func (i *Installer) readUsage(ctx context.Context, now time.Time) (reconcile.DiskUsage, error) {
	logs, err := storeBytes(ctx, i.client, logsStoreService, logsStorePort, logsStoreMetrics)
	if err != nil {
		return reconcile.DiskUsage{}, fmt.Errorf("openchoreo observability: logs store use: %w", err)
	}
	metrics, err := storeBytes(ctx, i.client, prometheusService, prometheusPort, metricsStoreUsage)
	if err != nil {
		return reconcile.DiskUsage{}, fmt.Errorf("openchoreo observability: metrics store use: %w", err)
	}
	return reconcile.DiskUsage{
		UsedMiB:     int(math.Ceil((logs + metrics) / (1 << 20))),
		CapacityMiB: capacityMiB,
		ReadAt:      now.UTC(),
	}, nil
}

// storeBytes sums names' samples on service's /metrics. Every name must
// be present at least once, so a renamed metric fails loudly rather than
// reporting zero.
func storeBytes(ctx context.Context, client *kubernetes.Client, service string, port int, names []string) (float64, error) {
	raw, err := client.GetRaw(ctx, kubernetes.ServiceProxyPath(PlaneNamespace, service, port)+"/metrics")
	if err != nil {
		var se *kubernetes.StatusError
		if errors.As(err, &se) {
			// Not the body, which can be long.
			return 0, fmt.Errorf("read %s's metrics: status %d", service, se.Code)
		}
		return 0, fmt.Errorf("read %s's metrics: %w", service, err)
	}
	sums, err := sumSamples(raw, names)
	if err != nil {
		return 0, fmt.Errorf("%s's metrics: %w", service, err)
	}
	total := 0.0
	for _, n := range names {
		v, ok := sums[n]
		if !ok {
			return 0, fmt.Errorf("%s's metrics have no %s", service, n)
		}
		total += v
	}
	return total, nil
}

// sumSamples sums every sample of names in Prometheus' text format,
// whatever their labels.
func sumSamples(text []byte, names []string) (map[string]float64, error) {
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	sums := map[string]float64{}
	sc := bufio.NewScanner(bytes.NewReader(text))
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' {
			continue
		}
		name, rest := line, ""
		if k := strings.IndexAny(line, "{ "); k >= 0 {
			name, rest = line[:k], line[k:]
		}
		if !want[name] {
			continue
		}
		if strings.HasPrefix(rest, "{") {
			end := strings.LastIndex(rest, "}")
			if end < 0 {
				return nil, fmt.Errorf("a %s sample has unclosed labels", name)
			}
			rest = rest[end+1:]
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			return nil, fmt.Errorf("a %s sample has no value", name)
		}
		v, err := strconv.ParseFloat(fields[0], 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
			return nil, fmt.Errorf("a %s sample has value %q", name, fields[0])
		}
		sums[name] += v
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return sums, nil
}
