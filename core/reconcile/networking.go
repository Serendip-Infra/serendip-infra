package reconcile

import (
	"context"
	"net/netip"
	"time"
)

// The networking capability (specs/025-networking plan decisions 1 and
// 2): what the cluster's network layer is and what it can show. Serendip
// observes it; it never installs or reconfigures the network layer, which
// is chosen when the cluster is created. Product names appear only as
// reported data, as ProviderID does elsewhere (constitution 1).

// PolicyEnforcement says whether the cluster enforces network policies.
type PolicyEnforcement string

const (
	PolicyEnforced    PolicyEnforcement = "enforced"
	PolicyNotEnforced PolicyEnforcement = "notEnforced"
	// PolicyUnknown: the network layer can't be asked, so Serendip
	// doesn't claim either (constitution 6).
	PolicyUnknown PolicyEnforcement = "unknown"
)

// NetworkFacts is what a networking provider observed.
type NetworkFacts struct {
	// Plugin and Version name the network layer as it reports itself;
	// Plugin is "unknown" when it can't be told.
	Plugin  string `json:"plugin"`
	Version string `json:"version,omitempty"`
	// PolicyEnforcement and PolicyReason say whether network policies
	// hold, and why Serendip thinks so, in its own words.
	PolicyEnforcement PolicyEnforcement `json:"policyEnforcement"`
	PolicyReason      string            `json:"policyReason,omitempty"`
	// Flows and RequestMetrics say whether connection records and
	// per-request metrics can be read.
	Flows          bool `json:"flows"`
	RequestMetrics bool `json:"requestMetrics"`
	// PodRanges are the ranges pod addresses come from, as far as the
	// network layer says (the notifier refuses them, RP-076).
	PodRanges []netip.Prefix `json:"podRanges,omitempty"`
	// Unavailable names what can't be read or doesn't match what
	// Serendip expects, each with its reason, such as "flows: the
	// cluster's network layer doesn't record connections".
	Unavailable []string `json:"unavailable,omitempty"`
	// ReadAt is when the facts were observed.
	ReadAt time.Time `json:"readAt"`
}

// NetworkingProvider is the networking capability's interface.
type NetworkingProvider interface {
	// Facts observes the network layer. It's read on an interval and
	// cached, never per request.
	Facts(ctx context.Context) (NetworkFacts, error)
}

// FlowSource is where the network layer writes its connection records on
// each node: the plain value one provider hands another through
// cmd/serendipd (provider-model.md's cooperation pattern), so the
// observability package can collect them without naming the networking
// provider. The zero value means there's nothing to collect.
type FlowSource struct {
	// HostPath is the directory holding the records on each node.
	HostPath string
	// File is the file name the records are appended to in HostPath.
	File string
	// Format names the record format, for the collector's parser.
	Format string
}

// IsZero reports whether there's nothing to collect.
func (s FlowSource) IsZero() bool { return s.HostPath == "" || s.File == "" }

// Flow verdicts.
const (
	FlowForwarded = "forwarded"
	FlowDropped   = "dropped"
)

// MaxFlowQueryLimit is the most flow records one page returns.
const MaxFlowQueryLimit = 500

// FlowQuery selects connection records, newest first. The API layer
// validates and caps it before a provider sees it.
type FlowQuery struct {
	From time.Time
	// To zero means now.
	To time.Time
	// DroppedOnly keeps only refused connections.
	DroppedOnly bool
	// Limit is capped at MaxFlowQueryLimit.
	Limit int
	// Before is the opaque cursor from the previous page's Next.
	Before string
}

// FlowEnd is one end of a connection. Component and Environment are set
// only when that end is a Component the caller may read: the API layer
// clears an end the caller can't, leaving Outside set (spec criterion 6).
type FlowEnd struct {
	Component   string `json:"component,omitempty"`
	Environment string `json:"environment,omitempty"`
	// Namespace is set for an end outside any Component (a platform
	// service), and cleared with the names for an unreadable one.
	Namespace string `json:"namespace,omitempty"`
	// Workload is the end's workload name, for a non-Component end.
	Workload string `json:"workload,omitempty"`
	// External marks an address outside the cluster.
	External bool `json:"external,omitempty"`
	// Outside marks an end the caller isn't allowed to see.
	Outside bool `json:"outside,omitempty"`
}

// Flow is one aggregated record: Count connections from Source to
// Destination on Port in the minute ending at Time.
type Flow struct {
	Time    time.Time `json:"time"`
	Verdict string    `json:"verdict"`
	// Reason is why a dropped connection was refused, in Serendip's
	// words ("refused by a network policy").
	Reason      string  `json:"reason,omitempty"`
	Source      FlowEnd `json:"source"`
	Destination FlowEnd `json:"destination"`
	Protocol    string  `json:"protocol"`
	Port        int     `json:"port,omitempty"`
	Count       int     `json:"count"`
}

// FlowPage is one page of records, newest first. Next works as LogPage's.
type FlowPage struct {
	Flows     []Flow `json:"flows"`
	Next      string `json:"next,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

// RequestSeries are a Deployment's HTTP request series: requests and
// failed requests (5xx) per second, and latency percentiles in
// milliseconds. A Component without an HTTP endpoint has none; Metrics'
// Unavailable says why.
type RequestSeries struct {
	Rate       []MetricPoint `json:"rate"`
	ErrorRate  []MetricPoint `json:"errorRate"`
	LatencyP50 []MetricPoint `json:"latencyP50"`
	LatencyP95 []MetricPoint `json:"latencyP95"`
	LatencyP99 []MetricPoint `json:"latencyP99"`
}

// TrafficEdge is the HTTP traffic from one Component to another in one
// Environment over a window: requests and failed requests per second.
type TrafficEdge struct {
	From      string  `json:"from"`
	To        string  `json:"to"`
	Rate      float64 `json:"rate"`
	ErrorRate float64 `json:"errorRate"`
}

// FlowReader is optional on an ObservabilityProvider whose package stores
// the network layer's connection records. Callers scope before calling:
// a provider reads only records with an end among targets, and marks the
// other end with its Component and Environment IDs for the API layer to
// keep or clear.
type FlowReader interface {
	Flows(ctx context.Context, targets []LogTarget, q FlowQuery) (FlowPage, error)
}

// TrafficReader is optional on an ObservabilityProvider that stores
// request metrics: the traffic between targets (all in one Environment)
// over [from, to). Edges with an end outside targets aren't returned.
type TrafficReader interface {
	Traffic(ctx context.Context, targets []LogTarget, from, to time.Time) ([]TrafficEdge, error)
}
