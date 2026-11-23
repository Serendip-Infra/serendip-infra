package api

import (
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/serendip-infra/serendip-infra/core/audit"
	"github.com/serendip-infra/serendip-infra/core/policy"
	"github.com/serendip-infra/serendip-infra/core/provider"
	"github.com/serendip-infra/serendip-infra/core/reconcile"
)

// tracingCapability is the capability the tracing package provides
// (spec 025).
const tracingCapability = "tracing"

// Caps on trace reads.
const (
	// defaultTraceLimit is a page's traces when ?limit is left out.
	defaultTraceLimit = 50
	// maxMinDuration bounds ?minDuration: no trace is kept that long.
	maxMinDuration = time.Hour
)

// WithTracing serves stored traces from the tracing capability (spec 025).
// Without it the trace routes answer 503.
func WithTracing(p reconcile.TracingProvider) ServerOption {
	return func(o *serverOptions) { o.tracing = p }
}

var tracesRead = policy.Action{Name: policy.ActionComponentTracesRead}

// TracesHandler serves a Deployment's stored traces (specs/025-networking
// plan decision 12). Scoping happens here, before the provider is asked:
// the provider reads only spans the collector attested to the targets it's
// given. It shares the observability reads' caps and time handling.
type TracesHandler struct {
	obs *ObservabilityHandler
	// provider is nil when no tracing package is wired.
	provider reconcile.TracingProvider
	ready    func() error
}

func newTracesHandler(obs *ObservabilityHandler, p reconcile.TracingProvider, providers *provider.Registry, packages reconcile.Catalogue) *TracesHandler {
	return &TracesHandler{obs: obs, provider: p,
		ready: packageReadiness(providers, packages, tracingCapability, "traces need "+packageNames(packages, tracingCapability, "the tracing package"))}
}

func (h *TracesHandler) Register(mux Mux) {
	mux.HandleFunc("GET /v1/deployments/{id}/traces", h.List)
	mux.HandleFunc("GET /v1/deployments/{id}/traces/{traceId}", h.Get)
}

// unavailable says why traces can't be read now, or nil.
func (h *TracesHandler) unavailable() error {
	if err := h.ready(); err != nil {
		return err
	}
	if h.provider == nil {
		return deployRefuse(http.StatusServiceUnavailable, "service unavailable", "traces aren't served by this server")
	}
	return nil
}

// traceQuery reads ?from, ?to, ?errorsOnly, ?minDuration, ?limit and
// ?before.
func (h *TracesHandler) traceQuery(r *http.Request) (reconcile.TraceQuery, error) {
	q := r.URL.Query()
	from, to, err := h.obs.timeRange(r, 0)
	if err != nil {
		return reconcile.TraceQuery{}, err
	}
	tq := reconcile.TraceQuery{From: from, To: to, Limit: defaultTraceLimit, Before: q.Get("before")}
	if s := q.Get("errorsOnly"); s != "" {
		if tq.ErrorsOnly, err = strconv.ParseBool(s); err != nil {
			return tq, errors.New("errorsOnly: true or false")
		}
	}
	if s := q.Get("minDuration"); s != "" {
		d, err := time.ParseDuration(s)
		if err != nil || d < 0 || d > maxMinDuration {
			return tq, errors.New("minDuration: a duration from 0 to " + maxMinDuration.String() + ", such as 250ms or 2s")
		}
		tq.MinDuration = d
	}
	if s := q.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > reconcile.MaxTraceQueryLimit {
			return tq, errors.New("limit: a whole number from 1 to " + strconv.Itoa(reconcile.MaxTraceQueryLimit))
		}
		tq.Limit = n
	}
	if _, err := reconcile.DecodeLogCursor(tq.Before); err != nil {
		return tq, errors.New("before: not a cursor from a search's next; start again without it")
	}
	return tq, nil
}

// tracesAnswer is a page of traces, with the window read.
type tracesAnswer struct {
	reconcile.TracePage
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}

// List answers one page of the traces with a span from a Deployment,
// newest first, paged as stored logs are.
func (h *TracesHandler) List(w http.ResponseWriter, r *http.Request) {
	q, err := h.traceQuery(r)
	if err != nil {
		WriteProblem(w, http.StatusBadRequest, "validation failed", err.Error(), audit.RequestIDFrom(r.Context()))
		return
	}
	if err := h.unavailable(); err != nil {
		h.obs.fail(w, r, err)
		return
	}
	d, err := h.obs.deployments.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		h.obs.fail(w, r, err)
		return
	}
	t, err := h.obs.target(r.Context(), d)
	if err != nil {
		h.obs.fail(w, r, err)
		return
	}
	ctx, done, ok := h.obs.begin(w, r)
	if !ok {
		return
	}
	defer done()
	page, err := h.provider.SearchTraces(ctx, []reconcile.LogTarget{t}, q)
	if err != nil {
		h.readFailed(w, r, err, ctx.Err())
		return
	}
	page.Traces = nonNil(page.Traces)
	writeJSON(w, http.StatusOK, tracesAnswer{TracePage: page, From: q.From, To: q.To})
}

// traceAnswer is one trace's spans the caller may read. Partial says the
// trace has spans from Components the caller can't read, which aren't
// shown; how many isn't said (plan decision 12).
type traceAnswer struct {
	TraceID   string           `json:"traceId"`
	Spans     []reconcile.Span `json:"spans"`
	Partial   bool             `json:"partial"`
	Truncated bool             `json:"truncated,omitempty"`
}

// Get answers one trace: the spans from every Deployment in the
// Deployment's Environment the caller may read traces of (this one
// included), in start order. None from any of them is 404, whatever other
// Components' spans exist, so a guessed trace ID tells nothing (spec
// criterion 9). A whole trace with no span from this Deployment is 404
// too: a trace is shown under a Component only if it's part of it (spec
// criterion 8; T012 found an Owner could open any trace of the
// Environment through any Deployment).
func (h *TracesHandler) Get(w http.ResponseWriter, r *http.Request) {
	reqID := audit.RequestIDFrom(r.Context())
	traceID := strings.ToLower(r.PathValue("traceId"))
	if !reconcile.ValidTraceID(traceID) {
		WriteProblem(w, http.StatusBadRequest, "validation failed", "traceId: a trace ID of 32 hexadecimal digits, not all zero", reqID)
		return
	}
	if err := h.unavailable(); err != nil {
		h.obs.fail(w, r, err)
		return
	}
	d, err := h.obs.deployments.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		h.obs.fail(w, r, err)
		return
	}
	t, err := h.obs.target(r.Context(), d)
	if err != nil {
		h.obs.fail(w, r, err)
		return
	}
	// The route checked the traces action on this Deployment; the others
	// in its Environment are checked one by one, and all are read in one
	// call. Past the cap, the rest count as unreadable.
	others, _, _, err := h.obs.envTargets(r.Context(), t.Environment, tracesRead, d.Metadata.ID, nil, reconcile.MaxLogTargets-1, false)
	if err != nil {
		h.obs.fail(w, r, err)
		return
	}
	ctx, done, ok := h.obs.begin(w, r)
	if !ok {
		return
	}
	defer done()
	tr, err := h.provider.Trace(ctx, append([]reconcile.LogTarget{t}, others...), traceID)
	if errors.Is(err, reconcile.ErrTraceNotFound) {
		WriteProblem(w, http.StatusNotFound, "not found", "trace "+traceID+" not found for Deployment/"+d.Metadata.ID, reqID)
		return
	}
	if err != nil {
		h.readFailed(w, r, err, ctx.Err())
		return
	}
	own := slices.ContainsFunc(tr.Spans, func(s reconcile.Span) bool {
		return s.Component == t.Component.Metadata.ID && s.Environment == t.Environment.Metadata.ID
	})
	if !own && tr.Truncated {
		// The cut may have dropped this Deployment's spans: ask for its
		// own alone (rare: a trace past MaxTraceSpans).
		_, err := h.provider.Trace(ctx, []reconcile.LogTarget{t}, traceID)
		switch {
		case err == nil:
			own = true
		case !errors.Is(err, reconcile.ErrTraceNotFound):
			h.readFailed(w, r, err, ctx.Err())
			return
		}
	}
	if !own {
		WriteProblem(w, http.StatusNotFound, "not found", "trace "+traceID+" not found for Deployment/"+d.Metadata.ID, reqID)
		return
	}
	// Hidden is never returned: partial says only that there's more.
	out := traceAnswer{TraceID: traceID, Spans: nonNil(tr.Spans), Partial: tr.Hidden > 0, Truncated: tr.Truncated}
	slices.SortStableFunc(out.Spans, func(a, b reconcile.Span) int { return a.Start.Compare(b.Start) })
	if len(out.Spans) > reconcile.MaxTraceSpans {
		out.Spans, out.Truncated = out.Spans[:reconcile.MaxTraceSpans], true
	}
	writeJSON(w, http.StatusOK, out)
}

// readFailed answers a failed trace read as the observability reads are
// answered, naming the tracing package. ctxErr is the query context's.
func (h *TracesHandler) readFailed(w http.ResponseWriter, r *http.Request, err, ctxErr error) {
	h.obs.readFailedFrom(w, r, readSource{ready: h.ready, pkg: "the tracing package", what: "stored traces"}, ctxErr, err)
}
