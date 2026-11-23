package reconcile

import (
	"context"
	"errors"
	"time"

	"github.com/serendip-infra/serendip-infra/core/resource"
)

// The tracing capability (specs/025-networking plan decisions 10 and 12):
// traces Components send, read after the requests that made them. It's
// its own optional package, so removing it leaves logs and metrics. It
// speaks in Serendip's terms; no backend's names appear here
// (constitution 1). It returns ErrProviderNotReady until its package is
// Ready.

// Caps on trace queries.
const (
	// MaxTraceQueryLimit is the most traces one page lists.
	MaxTraceQueryLimit = 200
	// MaxTraceSpans is the most spans one trace returns.
	MaxTraceSpans = 1000
	// MaxTraceIDLen bounds a trace ID from outside (W3C: 32 hex).
	MaxTraceIDLen = 32
)

// TraceQuery selects traces with a span from the targets, newest first.
// The API layer validates and caps it before a provider sees it.
type TraceQuery struct {
	From time.Time
	// To zero means now.
	To time.Time
	// ErrorsOnly keeps traces with a failed span.
	ErrorsOnly bool
	// MinDuration keeps traces at least this long; zero keeps all.
	MinDuration time.Duration
	// Limit is capped at MaxTraceQueryLimit.
	Limit int
	// Before is the opaque cursor from the previous page's Next.
	Before string
}

// TraceSummary is one trace as listed.
type TraceSummary struct {
	TraceID string    `json:"traceId"`
	Start   time.Time `json:"start"`
	// Duration is the whole trace's, as far as the stored spans say.
	Duration time.Duration `json:"durationNs"`
	// Root names the trace's first span, empty when it isn't stored
	// (another service started the trace, or its span was lost).
	Root      string `json:"root,omitempty"`
	Spans     int    `json:"spans"`
	HasErrors bool   `json:"hasErrors"`
}

// TracePage is one page of traces, newest first. Next works as LogPage's.
type TracePage struct {
	Traces    []TraceSummary `json:"traces"`
	Next      string         `json:"next,omitempty"`
	Truncated bool           `json:"truncated,omitempty"`
}

// Span is one stored span. Component and Environment are the IDs the
// collector attested from the sending pod, never what the span said of
// itself (spec criterion 8).
type Span struct {
	SpanID      string            `json:"spanId"`
	ParentID    string            `json:"parentId,omitempty"`
	Name        string            `json:"name"`
	Kind        string            `json:"kind,omitempty"`
	Start       time.Time         `json:"start"`
	Duration    time.Duration     `json:"durationNs"`
	Failed      bool              `json:"failed"`
	Status      string            `json:"status,omitempty"`
	Component   string            `json:"component"`
	Environment string            `json:"environment"`
	Attributes  map[string]string `json:"attributes,omitempty"`
}

// Trace is one trace's spans from the targets, in start order. Hidden
// counts the trace's spans from other Components, which aren't returned;
// it's for the API layer to say a trace is partial, never shown as a
// number (specs/025-networking plan decision 12).
type Trace struct {
	TraceID   string `json:"traceId"`
	Spans     []Span `json:"spans"`
	Hidden    int    `json:"hidden,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

// TracingProvider reads stored traces. Callers scope before calling: a
// provider reads only spans the collector attested to the targets.
type TracingProvider interface {
	SearchTraces(ctx context.Context, targets []LogTarget, q TraceQuery) (TracePage, error)
	// Trace returns traceID's spans from any of targets (at most
	// MaxLogTargets), and ErrTraceNotFound when they have none, whether or
	// not other Components' spans exist: a guessed trace ID tells nothing
	// (spec criterion 9).
	Trace(ctx context.Context, targets []LogTarget, traceID string) (Trace, error)
	// EvaluateCondition reads a trace condition, as
	// ObservabilityProvider.EvaluateCondition does.
	EvaluateCondition(ctx context.Context, t LogTarget, c resource.AlertCondition, window time.Duration) (ConditionValue, error)
}

// ErrTraceNotFound is a trace the target has no spans in.
var ErrTraceNotFound = errors.New("reconcile: no such trace for this Component")

// ValidTraceID reports whether id looks like a W3C trace ID: 32 lowercase
// hex digits, not all zero. Anything else never reaches a provider.
func ValidTraceID(id string) bool {
	if len(id) != MaxTraceIDLen {
		return false
	}
	zero := true
	for _, c := range id {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f':
		default:
			return false
		}
		if c != '0' {
			zero = false
		}
	}
	return !zero
}
