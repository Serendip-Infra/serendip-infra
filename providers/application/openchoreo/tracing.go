package openchoreo

import (
	"slices"
	"strings"

	"github.com/serendip-infra/serendip-infra/core/resource"
)

// Trace export (specs/025-networking plan decision 11): while the tracing
// capability is Ready, cmd/serendipd gives this provider the collector's
// endpoint, and a Component's next new release tells its workload where
// to send traces, in the OpenTelemetry SDKs' standard variables. The
// provider never names the tracing provider; it's given a URL.

const (
	envOTLPEndpoint = "OTEL_EXPORTER_OTLP_ENDPOINT"
	envServiceName  = "OTEL_SERVICE_NAME"
)

// SetTraceEndpoint sets where workloads send traces; "" stops new
// releases from carrying it. Releases already made keep what they have.
func (p *Provider) SetTraceEndpoint(endpoint string) {
	p.mu.Lock()
	p.traceEndpoint = endpoint
	p.mu.Unlock()
}

// TraceEndpoint is the endpoint SetTraceEndpoint set.
func (p *Provider) TraceEndpoint() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.traceEndpoint
}

// withTraceEnv is c with the trace variables added, and whether any was:
// none without an endpoint, and none the Component sets itself (it keeps
// its own).
func withTraceEnv(c workloadContainer, endpoint string, comp resource.Component) (workloadContainer, bool) {
	if endpoint == "" {
		return c, false
	}
	add := []envVar{{Key: envOTLPEndpoint, Value: endpoint}, {Key: envServiceName, Value: comp.Metadata.Name}}
	env := slices.Clone(c.Env)
	added := false
	for _, v := range add {
		if _, own := comp.Spec.Env[v.Key]; own {
			continue
		}
		env = append(env, v)
		added = true
	}
	if !added {
		return c, false
	}
	slices.SortFunc(env, func(a, b envVar) int { return strings.Compare(a.Key, b.Key) })
	c.Env = env
	return c, true
}
