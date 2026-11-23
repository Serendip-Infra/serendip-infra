package openchoreo

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/serendip-infra/serendip-infra/core/reconcile"
	"github.com/serendip-infra/serendip-infra/providers/kubernetes"
)

// The package's current disk use (plan decision 8, as amended by the
// findings from T002 and T006): the kubelet doesn't measure local-path
// volumes, so each store is asked for its own size:
//   - OpenObserve: its streams' stored and index sizes, from its streams
//     API. That needs the admin credentials, and the API server's Service
//     proxy strips an Authorization header (checked live), so the request
//     goes over a port-forward. Its own /metrics has no stored-size gauge
//     at v0.92.2.
//   - Prometheus: its blocks and its WAL, from its /metrics through the
//     Service proxy, with no credentials.

const (
	logsStoreService = "openobserve"

	prometheusService = "openchoreo-observability-prometheus"
	prometheusPort    = 9091

	// usageEvery bounds how often the stores are read (decision 8).
	usageEvery = 30 * time.Second
)

var metricsStoreUsage = []string{"prometheus_tsdb_storage_blocks_bytes", "prometheus_tsdb_wal_storage_size_bytes"}

// streamSizeUnit is the unit of the streams API's sizes: MiB. Checked
// live (T006): compressed_size plus index_size came to exactly the bytes
// of the stream's files.
const streamSizeUnit = 1 << 20

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
	// One read at a time: a second caller waits for the first's answer
	// rather than opening a second port-forward.
	i.usageMu.Lock()
	defer i.usageMu.Unlock()
	i.mu.Lock()
	last, lastErr, errAt = i.usage, i.usageErr, i.usageErrAt
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
	logs, err := i.logsStoreBytes(ctx)
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

// logsStoreOrg is the organization the logs module's Fluent Bit writes
// to (its URI /api/default/default/_json).
const logsStoreOrg = "default"

// logsStoreAuth reads the store's admin credentials from its Secret, as a
// Basic Authorization header. They're held only for the one request:
// never stored, logged or returned (plan decision 1.2), and sent only
// over the port-forward to the store's own pod.
func logsStoreAuth(ctx context.Context, client *kubernetes.Client) (http.Header, error) {
	obj, err := client.Get(ctx, "/api/v1/namespaces/"+PlaneNamespace+"/secrets/"+adminSecretName)
	if errors.Is(err, kubernetes.ErrNotFound) {
		return nil, fmt.Errorf("the logs store's credentials Secret %s is missing: %w", adminSecretName, err)
	}
	if err != nil {
		return nil, fmt.Errorf("read the logs store's credentials: %v", trimErr(err))
	}
	var data map[string][]byte
	if err := json.Unmarshal(obj.Data, &data); err != nil {
		return nil, fmt.Errorf("decode the logs store's credentials: %w", err)
	}
	user, pass := string(data["ZO_ROOT_USER_EMAIL"]), string(data["ZO_ROOT_USER_PASSWORD"])
	if user == "" || pass == "" {
		return nil, errors.New("the logs store's credentials Secret is missing a key")
	}
	h := http.Header{}
	h.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(user+":"+pass)))
	return h, nil
}

// logsHTTPClient reaches the logs store's API over a port-forward, one
// connection a request.
// The caller calls the returned func when done.
func (i *Installer) logsHTTPClient() (*http.Client, func()) {
	if i.logsHTTP != nil {
		return i.logsHTTP, func() {}
	}
	return storeHTTPClient(i.client)
}

// logsStoreBytes sums the logs streams' stored and index sizes. The store
// updates them when it writes its files, so they lag new lines by minutes.
func (i *Installer) logsStoreBytes(ctx context.Context) (float64, error) {
	auth, err := logsStoreAuth(ctx, i.client)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("http://%s.%s:%d/api/%s/streams?type=logs", logsStoreService, PlaneNamespace, logsStorePort, logsStoreOrg), nil)
	if err != nil {
		return 0, err
	}
	req.Header = auth
	hc, done := i.logsHTTPClient()
	defer done()
	resp, err := hc.Do(req)
	if err != nil {
		return 0, fmt.Errorf("read the logs store's streams: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("read the logs store's streams: status %d", resp.StatusCode)
	}
	var out struct {
		List []struct {
			Stats *struct {
				CompressedSize *float64 `json:"compressed_size"`
				IndexSize      *float64 `json:"index_size"`
			} `json:"stats"`
		} `json:"list"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&out); err != nil {
		return 0, fmt.Errorf("decode the logs store's streams: %w", err)
	}
	total := 0.0
	for _, s := range out.List {
		if s.Stats == nil {
			// A stream with nothing stored yet.
			continue
		}
		if s.Stats.CompressedSize == nil || s.Stats.IndexSize == nil {
			// A renamed field fails loudly rather than reading as zero.
			return 0, errors.New("the logs store's streams have no compressed_size or index_size")
		}
		for _, v := range []float64{*s.Stats.CompressedSize, *s.Stats.IndexSize} {
			if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
				return 0, fmt.Errorf("the logs store reports a size of %v", v)
			}
			total += v
		}
	}
	return total * streamSizeUnit, nil
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
