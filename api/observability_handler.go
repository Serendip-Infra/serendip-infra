package api

import (
	"cmp"
	"context"
	"errors"
	"log"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/serendip-infra/serendip-infra/core/audit"
	"github.com/serendip-infra/serendip-infra/core/policy"
	"github.com/serendip-infra/serendip-infra/core/provider"
	"github.com/serendip-infra/serendip-infra/core/reconcile"
	"github.com/serendip-infra/serendip-infra/core/resource"
	"github.com/serendip-infra/serendip-infra/core/store"
)

// observabilityCapability is the capability the observability package
// provides (spec 023).
const observabilityCapability = "observability"

// Caps on stored-log and metrics reads (specs/023-observability plan
// decision 11): the same numbers as spec 020's log streams (RP-031), with
// their own allowance, and a deadline on each query.
const (
	maxObsPerCaller = 3
	maxObsPerServer = 20
	obsQueryTimeout = 20 * time.Second
	// defaultLogLimit is a page's lines when ?limit is left out.
	defaultSearchLimit = 100
	// maxSearchText bounds the phrase searched for.
	maxSearchText = 256
	// defaultObsWindow is the range read when ?from is left out.
	defaultObsWindow = time.Hour
	// minMetricsRange is the shortest metrics range (plan decision 7).
	minMetricsRange = 15 * time.Minute
	// defaultRetention is the longest range read when the catalogue
	// doesn't state the package's retention.
	defaultRetention = 3 * 24 * time.Hour
	// summaryPoints is the most points per series with ?summary=true, so
	// an assistant gets a summary rather than 300 samples (decision 11).
	summaryPoints = 60
)

// ObservabilityHandler serves stored logs and metrics (spec 023): a
// Deployment's logs, an Application's logs in an Environment, and a
// Deployment's CPU and memory. Reads only, so nothing is stored or
// audited (spec item 14). Scoping happens here, before the provider is
// asked (plan decision 6): the provider reads only the targets it's given.
type ObservabilityHandler struct {
	deployments store.Store[resource.DeploymentSpec, resource.DeploymentStatus]
	envs        store.Store[resource.EnvironmentSpec, resource.EnvironmentStatus]
	comps       store.Store[resource.ComponentSpec, resource.ComponentStatus]
	apps        store.Store[resource.ApplicationSpec, resource.ApplicationStatus]
	// provider is nil when no observability package is wired.
	provider reconcile.ObservabilityProvider
	// ready says why stored logs and metrics can't be read now.
	ready func() error
	// facts is the network layer's cached facts (spec 025); nil when
	// this server doesn't read them.
	facts NetworkFactsFunc
	// retention is the longest range a query may cover.
	retention time.Duration
	limit     *streamLimiter
	now       func() time.Time
}

func newObservabilityHandler(deployments store.Store[resource.DeploymentSpec, resource.DeploymentStatus], envs store.Store[resource.EnvironmentSpec, resource.EnvironmentStatus],
	comps store.Store[resource.ComponentSpec, resource.ComponentStatus], apps store.Store[resource.ApplicationSpec, resource.ApplicationStatus],
	p reconcile.ObservabilityProvider, providers *provider.Registry, packages reconcile.Catalogue) *ObservabilityHandler {
	return &ObservabilityHandler{deployments: deployments, envs: envs, comps: comps, apps: apps, provider: p,
		ready:     observabilityReadiness(providers, packages),
		retention: observabilityRetention(packages),
		limit:     &streamLimiter{perCaller: maxObsPerCaller, total: maxObsPerServer, open: map[string]int{}},
		now:       time.Now}
}

func (h *ObservabilityHandler) Register(mux Mux) {
	mux.HandleFunc("GET /v1/deployments/{id}/logs/search", h.SearchDeployment)
	mux.HandleFunc("GET /v1/applications/{id}/logs/search", h.SearchApplication)
	mux.HandleFunc("GET /v1/deployments/{id}/metrics", h.Metrics)
	mux.HandleFunc("GET /v1/deployments/{id}/flows", h.Flows)
	mux.HandleFunc("GET /v1/applications/{id}/traffic", h.Traffic)
}

// packageNames names the catalogue's packages for capability, for
// messages: "the observability package (a or b)".
func packageNames(packages reconcile.Catalogue, capability, what string) string {
	var names []string
	for _, p := range packages {
		if p.Capability == capability {
			names = append(names, p.Provider)
		}
	}
	slices.Sort(names)
	if len(names) > 0 {
		what += " (" + strings.Join(names, " or ") + ")"
	}
	return what
}

// observabilityReadiness says why stored logs and metrics can't be read:
// 503 naming the package, and whether it's not installed, installing or
// not ready (spec item 10).
func observabilityReadiness(providers *provider.Registry, packages reconcile.Catalogue) func() error {
	return packageReadiness(providers, packages, observabilityCapability, "stored logs and metrics need "+packageNames(packages, observabilityCapability, "the observability package"))
}

// packageReadiness says why capability's package can't be read from:
// needs (what needs which package) and the package's state, as a 503.
func packageReadiness(providers *provider.Registry, packages reconcile.Catalogue, capability, needs string) func() error {
	return func() error {
		var rd provider.Readiness
		if providers != nil {
			rd = providers.Readiness()[capability]
		}
		if rd.Ready {
			return nil
		}
		var state string
		switch rd.Reason {
		case "", reconcile.ReasonNotInstalled:
			state = "it isn't installed; install it by creating a ProviderInstance naming it (GET /v1/packages)"
		case reconcile.ReasonInstalling:
			state = "it's installing; retry shortly"
		case reconcile.ReasonRemoving:
			state = "it's being removed"
		case reconcile.ReasonUnavailable:
			state = "it can't be installed in this build: " + cmp.Or(rd.Message, "not available yet")
		default:
			state = "it's installed but not ready (" + rd.Reason + ")"
		}
		return deployRefuse(http.StatusServiceUnavailable, "service unavailable", "%s installed and ready; %s", needs, state)
	}
}

// observabilityRetention is the longest range a query may cover: the
// package's stated retention, else defaultRetention.
func observabilityRetention(packages reconcile.Catalogue) time.Duration {
	days := 0
	for _, p := range packages {
		if p.Capability == observabilityCapability && p.Requirements != nil && p.Requirements.Disk != nil {
			days = max(days, p.Requirements.Disk.RetentionDays)
		}
	}
	if days == 0 {
		return defaultRetention
	}
	return time.Duration(days) * 24 * time.Hour
}

func (h *ObservabilityHandler) fail(w http.ResponseWriter, r *http.Request, err error) {
	var rf *deployRefusal
	if errors.As(err, &rf) {
		if rf.status == http.StatusServiceUnavailable {
			w.Header().Set("Retry-After", "10")
		}
		WriteProblem(w, rf.status, rf.title, rf.detail, audit.RequestIDFrom(r.Context()))
		return
	}
	writeStoreErr(w, r, err)
}

// unavailable says why stored logs and metrics can't be read now, or nil.
// Each handler checks it before answering anything else, so a missing
// package is never mistaken for "no logs".
func (h *ObservabilityHandler) unavailable() error {
	if err := h.ready(); err != nil {
		return err
	}
	if h.provider == nil {
		// Ready, but this server wasn't given the capability.
		return deployRefuse(http.StatusServiceUnavailable, "service unavailable", "stored logs and metrics aren't served by this server")
	}
	return nil
}

// readSource names what a read was of, for readFailed's answers: the
// package's readiness, the package and the data, in Serendip's words.
type readSource struct {
	ready     func() error
	pkg, what string
}

func (h *ObservabilityHandler) source() readSource {
	return readSource{ready: h.ready, pkg: "the observability package", what: "stored logs and metrics"}
}

// readFailed answers a failed provider read under the query's context
// ctx. Never the error's text, which can name the backend's internals;
// it's logged for the operator. A client that went away gets nothing.
func (h *ObservabilityHandler) readFailed(w http.ResponseWriter, r *http.Request, ctx context.Context, err error) {
	h.readFailedFrom(w, r, h.source(), ctx.Err(), err)
}

// readFailedFrom is readFailed for src, given the query context's error.
func (h *ObservabilityHandler) readFailedFrom(w http.ResponseWriter, r *http.Request, src readSource, ctxErr, err error) {
	reqID := audit.RequestIDFrom(r.Context())
	switch {
	case r.Context().Err() != nil:
		return
	case errors.Is(err, reconcile.ErrProviderNotReady):
		if rerr := src.ready(); rerr != nil {
			h.fail(w, r, rerr)
			return
		}
		w.Header().Set("Retry-After", "10")
		WriteProblem(w, http.StatusServiceUnavailable, "service unavailable", src.pkg+" is still being set up; retry shortly", reqID)
	case errors.Is(err, reconcile.ErrNotCollected):
		// The facts said yes, the package says no (spec 025).
		WriteProblem(w, http.StatusConflict, "not available", "this isn't collected on this platform now; GET /v1/networking says what the network layer provides", reqID)
	case errors.Is(err, reconcile.ErrBadCursor):
		WriteProblem(w, http.StatusBadRequest, "validation failed", "before: not a cursor from this search's next; start again without it", reqID)
	case errors.Is(ctxErr, context.DeadlineExceeded):
		WriteProblem(w, http.StatusGatewayTimeout, "gateway timeout", "the query took longer than "+obsQueryTimeout.String()+"; narrow the time range", reqID)
	default:
		log.Printf("api: %s %s: %s read failed (request %s): %v", r.Method, r.URL.Path, src.what, reqID, err)
		WriteProblem(w, http.StatusBadGateway, "bad gateway", src.what+" couldn't be read; retry later, quoting the request ID", reqID)
	}
}

// begin takes one of the caller's query slots, answering the error itself
// when it can't. done releases the slot and the query's deadline.
// Readiness is checked earlier, by each handler (unavailable).
func (h *ObservabilityHandler) begin(w http.ResponseWriter, r *http.Request) (ctx context.Context, done func(), ok bool) {
	reqID := audit.RequestIDFrom(r.Context())
	caller, found := callerFrom(r.Context())
	if !found {
		WriteProblem(w, http.StatusUnauthorized, "unauthorized", "missing or invalid bearer token", reqID)
		return nil, nil, false
	}
	if !h.limit.acquire(caller.ID) {
		w.Header().Set("Retry-After", "5")
		WriteProblem(w, http.StatusTooManyRequests, "too many requests",
			"too many log and metrics queries at once: at most "+strconv.Itoa(h.limit.perCaller)+" per caller and "+strconv.Itoa(h.limit.total)+" in all; retry shortly", reqID)
		return nil, nil, false
	}
	ctx, cancel := context.WithTimeout(r.Context(), obsQueryTimeout)
	var once sync.Once
	return ctx, func() { once.Do(func() { cancel(); h.limit.release(caller.ID) }) }, true
}

// timeRange reads ?from and ?to (RFC 3339), defaulting to the last
// defaultObsWindow, and checks the range is at least shortest and within
// the retention period.
func (h *ObservabilityHandler) timeRange(r *http.Request, shortest time.Duration) (from, to time.Time, err error) {
	q := r.URL.Query()
	now := h.now().UTC()
	to = now
	if s := q.Get("to"); s != "" {
		if to, err = time.Parse(time.RFC3339, s); err != nil {
			return from, to, errors.New("to: an RFC 3339 time, e.g. 2026-11-14T10:00:00Z")
		}
		if to.After(now) {
			to = now
		}
	}
	from = to.Add(-defaultObsWindow)
	named := "from"
	if s := q.Get("from"); s != "" {
		if from, err = time.Parse(time.RFC3339, s); err != nil {
			return from, to, errors.New("from: an RFC 3339 time, e.g. 2026-11-14T09:00:00Z (a + in its offset must be sent as %2B)")
		}
	} else {
		named = "to" // from was an hour before it
	}
	if !from.Before(to) {
		return from, to, errors.New("from: must be before to (and before now)")
	}
	if to.Sub(from) < shortest {
		return from, to, errors.New("from and to: at least " + shortest.String() + " apart")
	}
	if now.Sub(from) > h.retention {
		return from, to, errors.New(named + ": data is kept for " + h.retention.String() + ", so the range must start within it")
	}
	return from, to, nil
}

// logQuery reads a search's query: ?q, ?from, ?to, ?limit and ?before.
func (h *ObservabilityHandler) logQuery(r *http.Request) (reconcile.LogQuery, error) {
	q := r.URL.Query()
	from, to, err := h.timeRange(r, 0)
	if err != nil {
		return reconcile.LogQuery{}, err
	}
	lq := reconcile.LogQuery{From: from, To: to, Text: q.Get("q"), Limit: defaultSearchLimit, Before: q.Get("before")}
	if len(lq.Text) > maxSearchText || strings.ContainsFunc(lq.Text, unicode.IsControl) {
		return lq, errors.New("q: a plain phrase of at most " + strconv.Itoa(maxSearchText) + " bytes, without control characters")
	}
	if s := q.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > reconcile.MaxLogQueryLimit {
			return lq, errors.New("limit: a whole number from 1 to " + strconv.Itoa(reconcile.MaxLogQueryLimit))
		}
		lq.Limit = n
	}
	if _, err := reconcile.DecodeLogCursor(lq.Before); err != nil {
		return lq, errors.New("before: not a cursor from a search's next; start again without it")
	}
	return lq, nil
}

// target is the Deployment's LogTarget: it, its Environment and the
// Component it runs.
func (h *ObservabilityHandler) target(ctx context.Context, d resource.Deployment) (reconcile.LogTarget, error) {
	env, err := h.envs.Get(ctx, owner(d.Metadata, resource.KindEnvironment))
	if err != nil {
		return reconcile.LogTarget{}, err
	}
	comp, err := h.comps.Get(ctx, d.Spec.Component)
	if err != nil {
		return reconcile.LogTarget{}, err
	}
	return reconcile.LogTarget{Deployment: d, Component: comp, Environment: env}, nil
}

// searchAnswer is a page of stored lines. Omitted, on an Application's
// search, counts the Deployments left out because the caller may not read
// their logs, without naming them (plan decision 6).
type searchAnswer struct {
	reconcile.LogPage
	Omitted *int `json:"omitted,omitempty"`
}

// SearchDeployment answers one page of a Deployment's stored lines, newest
// first: {"lines", "next", "truncated"}. Only an empty next ends a search;
// a page can be short, even empty, with next set.
func (h *ObservabilityHandler) SearchDeployment(w http.ResponseWriter, r *http.Request) {
	q, err := h.logQuery(r)
	if err != nil {
		WriteProblem(w, http.StatusBadRequest, "validation failed", err.Error(), audit.RequestIDFrom(r.Context()))
		return
	}
	if err := h.unavailable(); err != nil {
		h.fail(w, r, err)
		return
	}
	d, err := h.deployments.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	t, err := h.target(r.Context(), d)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	ctx, done, ok := h.begin(w, r)
	if !ok {
		return
	}
	defer done()
	page, err := h.provider.SearchLogs(ctx, []reconcile.LogTarget{t}, q)
	if err != nil {
		h.readFailed(w, r, ctx, err)
		return
	}
	writeJSON(w, http.StatusOK, searchAnswer{LogPage: nonNilPage(page)})
}

var (
	logsRead        = policy.Action{Name: policy.ActionComponentLogsRead}
	environmentRead = action(resource.KindEnvironment, "read")
)

// SearchApplication answers one page of the stored lines of an
// Application's Deployments in ?environment, newest first, as
// SearchDeployment does, plus omitted. The route checked reading the
// Application; only the Deployments whose logs the caller may read become
// targets, and none is a 403.
func (h *ObservabilityHandler) SearchApplication(w http.ResponseWriter, r *http.Request) {
	reqID := audit.RequestIDFrom(r.Context())
	q, err := h.logQuery(r)
	if err != nil {
		WriteProblem(w, http.StatusBadRequest, "validation failed", err.Error(), reqID)
		return
	}
	envID := r.URL.Query().Get("environment")
	if envID == "" {
		WriteProblem(w, http.StatusBadRequest, "validation failed", "environment: the ID of the Environment to search in is required", reqID)
		return
	}
	if err := h.unavailable(); err != nil {
		h.fail(w, r, err)
		return
	}
	app, err := h.apps.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	env, ok := h.readableEnvironment(w, r, envID)
	if !ok {
		return
	}
	deps, err := h.deployments.List(r.Context(), store.ListOptions{OwnerID: env.Metadata.ID})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	slices.SortFunc(deps, func(a, b resource.Deployment) int {
		return cmp.Or(strings.Compare(a.Metadata.Name, b.Metadata.Name), strings.Compare(a.Metadata.ID, b.Metadata.ID))
	})
	var targets []reconcile.LogTarget
	omitted := 0
	for _, d := range deps {
		comp, err := h.comps.Get(r.Context(), d.Spec.Component)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			h.fail(w, r, err)
			return
		}
		if owner(comp.Metadata, resource.KindApplication) != app.Metadata.ID {
			continue
		}
		ok, err := allowedAt(r.Context(), logsRead, resource.Scope{Kind: resource.KindDeployment, ID: d.Metadata.ID})
		if err != nil {
			h.fail(w, r, err)
			return
		}
		if !ok {
			omitted++
			continue
		}
		targets = append(targets, reconcile.LogTarget{Deployment: d, Component: comp, Environment: env})
	}
	if len(targets) == 0 && omitted > 0 {
		WriteProblem(w, http.StatusForbidden, "forbidden", "missing "+policy.ActionComponentLogsRead+" on every Deployment of Application/"+app.Metadata.ID+" in Environment/"+envID, reqID)
		return
	}
	if len(targets) > reconcile.MaxLogTargets {
		WriteProblem(w, http.StatusUnprocessableEntity, "too many components",
			"the Application has "+strconv.Itoa(len(targets))+" Deployments here, and a search reads at most "+strconv.Itoa(reconcile.MaxLogTargets)+"; search a Deployment's logs instead", reqID)
		return
	}
	if len(targets) == 0 {
		writeJSON(w, http.StatusOK, searchAnswer{LogPage: reconcile.LogPage{Lines: []reconcile.StoredLogLine{}}, Omitted: &omitted})
		return
	}
	ctx, done, ok := h.begin(w, r)
	if !ok {
		return
	}
	defer done()
	page, err := h.provider.SearchLogs(ctx, targets, q)
	if err != nil {
		h.readFailed(w, r, ctx, err)
		return
	}
	writeJSON(w, http.StatusOK, searchAnswer{LogPage: nonNilPage(page), Omitted: &omitted})
}

// readableEnvironment reads Environment envID, answering 404 itself when
// it's missing or the caller can't read it: one answer for both, so an
// Environment's existence isn't told to someone who can't read it.
func (h *ObservabilityHandler) readableEnvironment(w http.ResponseWriter, r *http.Request, envID string) (resource.Environment, bool) {
	env, err := h.envs.Get(r.Context(), envID)
	if err == nil {
		var readable bool
		readable, err = allowedAt(r.Context(), environmentRead, resource.Scope{Kind: resource.KindEnvironment, ID: envID})
		if err == nil && !readable {
			err = store.ErrNotFound
		}
	}
	if errors.Is(err, store.ErrNotFound) {
		WriteProblem(w, http.StatusNotFound, "not found", "environment: Environment "+envID+" not found", audit.RequestIDFrom(r.Context()))
		return env, false
	}
	if err != nil {
		h.fail(w, r, err)
		return env, false
	}
	return env, true
}

func nonNilPage(p reconcile.LogPage) reconcile.LogPage {
	p.Lines = nonNil(p.Lines)
	return p
}

// seriesSummary is one series in brief, for a reader who wants the gist:
// an assistant diagnosing (spec item 11).
type seriesSummary struct {
	Min    float64 `json:"min"`
	Avg    float64 `json:"avg"`
	Max    float64 `json:"max"`
	Latest float64 `json:"latest"`
	Points int     `json:"points"`
}

// metricsAnswer is a Deployment's metrics, with each series summarised.
type metricsAnswer struct {
	reconcile.Metrics
	From    time.Time                 `json:"from"`
	To      time.Time                 `json:"to"`
	Summary map[string]*seriesSummary `json:"summary"`
}

// finiteNumber reports whether v can be carried in JSON.
func finiteNumber(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// finite is s without the samples JSON can't carry, never nil.
func finite(s []reconcile.MetricPoint) []reconcile.MetricPoint {
	out := make([]reconcile.MetricPoint, 0, len(s))
	for _, p := range s {
		if !math.IsNaN(p.Value) && !math.IsInf(p.Value, 0) {
			out = append(out, p)
		}
	}
	return out
}

func summarise(s []reconcile.MetricPoint) *seriesSummary {
	if len(s) == 0 {
		return nil
	}
	out := &seriesSummary{Min: s[0].Value, Max: s[0].Value, Latest: s[len(s)-1].Value, Points: len(s)}
	sum := 0.0
	for _, p := range s {
		out.Min, out.Max = min(out.Min, p.Value), max(out.Max, p.Value)
		sum += p.Value
	}
	out.Avg = sum / float64(len(s))
	return out
}

// downsample averages s into at most n points, each at its bucket's last
// time. The summary keeps the true minimum and maximum.
func downsample(s []reconcile.MetricPoint, n int) []reconcile.MetricPoint {
	if len(s) <= n {
		return s
	}
	size := (len(s) + n - 1) / n
	out := make([]reconcile.MetricPoint, 0, n)
	for i := 0; i < len(s); i += size {
		bucket := s[i:min(i+size, len(s))]
		sum := 0.0
		for _, p := range bucket {
			sum += p.Value
		}
		out = append(out, reconcile.MetricPoint{Time: bucket[len(bucket)-1].Time, Value: sum / float64(len(bucket))})
	}
	return out
}

// Metrics answers a Deployment's CPU (millicores) and memory (MiB) over
// ?from to ?to (the last hour by default, at least 15 minutes), its
// requests and limits where known, its HTTP requests where the network
// layer records them (spec 025), what isn't available and why, and a
// summary of each series. ?summary=true keeps at most 60 points a series.
func (h *ObservabilityHandler) Metrics(w http.ResponseWriter, r *http.Request) {
	reqID := audit.RequestIDFrom(r.Context())
	from, to, err := h.timeRange(r, minMetricsRange)
	if err != nil {
		WriteProblem(w, http.StatusBadRequest, "validation failed", err.Error(), reqID)
		return
	}
	brief := false
	if s := r.URL.Query().Get("summary"); s != "" {
		if brief, err = strconv.ParseBool(s); err != nil {
			WriteProblem(w, http.StatusBadRequest, "validation failed", "summary: true or false", reqID)
			return
		}
	}
	if err := h.unavailable(); err != nil {
		h.fail(w, r, err)
		return
	}
	d, err := h.deployments.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	t, err := h.target(r.Context(), d)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	ctx, done, ok := h.begin(w, r)
	if !ok {
		return
	}
	defer done()
	m, err := h.provider.Metrics(ctx, t, reconcile.MetricsQuery{From: from, To: to})
	if err != nil {
		h.readFailed(w, r, ctx, err)
		return
	}
	// A sample JSON can't carry (NaN, ±Inf) would fail the whole answer.
	m.CPU, m.Memory = finite(m.CPU), finite(m.Memory)
	out := metricsAnswer{From: from, To: to, Summary: map[string]*seriesSummary{}}
	if s := summarise(m.CPU); s != nil {
		out.Summary["cpu"] = s
	}
	if s := summarise(m.Memory); s != nil {
		out.Summary["memory"] = s
	}
	if rq := m.Requests; rq != nil {
		// Spec 025: the HTTP request series, treated as CPU and memory are.
		series := []struct {
			name string
			s    *[]reconcile.MetricPoint
		}{{"requests.rate", &rq.Rate}, {"requests.errorRate", &rq.ErrorRate}, {"requests.latencyP50", &rq.LatencyP50},
			{"requests.latencyP95", &rq.LatencyP95}, {"requests.latencyP99", &rq.LatencyP99}}
		for _, e := range series {
			*e.s = finite(*e.s)
			if s := summarise(*e.s); s != nil {
				out.Summary[e.name] = s
			}
			if brief {
				*e.s = downsample(*e.s, summaryPoints)
			}
		}
	}
	if brief {
		m.CPU, m.Memory = downsample(m.CPU, summaryPoints), downsample(m.Memory, summaryPoints)
	}
	out.Metrics = m
	writeJSON(w, http.StatusOK, out)
}
