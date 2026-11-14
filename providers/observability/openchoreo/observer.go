package openchoreo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/serendip-infra/serendip-infra/providers/kubernetes"
)

// The client for OpenChoreo's observability adapters (plan decision 4 as
// amended by spike findings 3–5 and 10): the logs and metrics adapters'
// query API, reached through the API server's Service proxy with
// serendipd's own credentials. The Observer itself isn't queried.
//
// The request and response shapes are the adapters' OpenAPI at v1.3.1
// (openapi/observability-logs-adapter-api.yaml and
// observability-metrics-adapter.yaml; VERSIONS.md).

const (
	logsAdapterService = "logs-adapter"
	logsAdapterPort    = 9098

	// adapterMaxLimit is the most lines the logs API's schema allows in one
	// answer. The OpenObserve adapter doesn't enforce it, so it's enforced
	// here.
	adapterMaxLimit = 1000
)

// logsRequest is POST /api/v1/logs/query's body. Its searchScope is a
// oneOf without a discriminator: the adapter reads it as a workflow scope
// when workflowRunName is set, and as a component scope otherwise.
type logsRequest struct {
	StartTime    string `json:"startTime"`
	EndTime      string `json:"endTime"`
	Limit        int    `json:"limit"`
	SortOrder    string `json:"sortOrder"`
	SearchPhrase string `json:"searchPhrase,omitempty"`
	SearchScope  any    `json:"searchScope"`
}

// workflowScope is a build run's scope. Namespace is the OpenChoreo
// namespace the run was created in; the adapter adds "workflows-" itself.
type workflowScope struct {
	Namespace       string `json:"namespace"`
	WorkflowRunName string `json:"workflowRunName"`
}

// logEntry is one answer line. A workflow-scope entry has only Timestamp
// and Log.
type logEntry struct {
	Timestamp string `json:"timestamp"`
	Log       string `json:"log"`
	Level     string `json:"level"`
	Metadata  struct {
		ComponentUID   string `json:"componentUid"`
		EnvironmentUID string `json:"environmentUid"`
		ProjectUID     string `json:"projectUid"`
		PodName        string `json:"podName"`
		ContainerName  string `json:"containerName"`
	} `json:"metadata"`
}

type logsResponse struct {
	Logs []logEntry `json:"logs"`
}

// metricsRequest is POST /api/v1/metrics/query's body.
type metricsRequest struct {
	Metric      string         `json:"metric"`
	StartTime   string         `json:"startTime"`
	EndTime     string         `json:"endTime"`
	Step        string         `json:"step,omitempty"`
	SearchScope componentScope `json:"searchScope"`
}

// point is one sample of a series. CPU is in cores and memory in bytes
// (the adapter's PromQL: cAdvisor's rates and working set, and
// kube-state-metrics' requests and limits, unconverted).
type point struct {
	Timestamp string  `json:"timestamp"`
	Value     float64 `json:"value"`
}

// resourceMetrics is metric=resource's answer, the series this package
// reads.
type resourceMetrics struct {
	CPUUsage       []point `json:"cpuUsage"`
	CPURequests    []point `json:"cpuRequests"`
	CPULimits      []point `json:"cpuLimits"`
	MemoryUsage    []point `json:"memoryUsage"`
	MemoryRequests []point `json:"memoryRequests"`
	MemoryLimits   []point `json:"memoryLimits"`
}

// adapters queries the two adapters.
type adapters struct {
	client *kubernetes.Client
}

func (a adapters) queryLogs(ctx context.Context, req logsRequest) ([]logEntry, error) {
	path := kubernetes.ServiceProxyPath(PlaneNamespace, logsAdapterService, logsAdapterPort) + "/api/v1/logs/query"
	raw, err := a.client.PostRaw(ctx, path, req)
	if err != nil {
		return nil, adapterError("logs", err)
	}
	var out logsResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("openchoreo observability: decode the logs answer: %w", err)
	}
	return out.Logs, nil
}

func (a adapters) queryResourceMetrics(ctx context.Context, req metricsRequest) (resourceMetrics, error) {
	path := kubernetes.ServiceProxyPath(PlaneNamespace, metricsAdapterService, metricsAdapterPort) + "/api/v1/metrics/query"
	raw, err := a.client.PostRaw(ctx, path, req)
	if err != nil {
		return resourceMetrics{}, adapterError("metrics", err)
	}
	var out resourceMetrics
	if err := json.Unmarshal(raw, &out); err != nil {
		return resourceMetrics{}, fmt.Errorf("openchoreo observability: decode the metrics answer: %w", err)
	}
	return out, nil
}

// adapterError words a failed query without passing on the adapter's
// body, which may echo the query.
func adapterError(what string, err error) error {
	var se *kubernetes.StatusError
	if errors.As(err, &se) {
		return fmt.Errorf("openchoreo observability: the %s query failed with status %d", what, se.Code)
	}
	if errors.Is(err, kubernetes.ErrNotFound) {
		return fmt.Errorf("openchoreo observability: the %s adapter isn't reachable (not found)", what)
	}
	return fmt.Errorf("openchoreo observability: %s query: %w", what, err)
}

// adapterTime formats a bound for the adapters. They read RFC 3339 and
// truncate to microseconds, the stores' precision.
func adapterTime(t time.Time) string {
	return t.UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano)
}

// parseAdapterTime reads an answer's timestamp, at the stores'
// microsecond precision.
func parseAdapterTime(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, err
	}
	return t.UTC().Truncate(time.Microsecond), nil
}
