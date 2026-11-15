package api

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/serendip-infra/serendip-infra/core/audit"
	"github.com/serendip-infra/serendip-infra/core/resource"
	"github.com/serendip-infra/serendip-infra/core/store"
)

// Alerts and rule silences (specs/024-alerts plan decision 12). Alerts are
// opened and resolved only by serendipd; a person reads them (scoped to
// what they may read, Alert by Alert), and acknowledges them. Silencing
// changes a rule's status, through its own action.

// Alert list pages.
const (
	defaultAlertLimit = 100
	maxAlertLimit     = 500
)

type alertsHandler struct {
	alerts store.Store[resource.AlertSpec, resource.AlertStatus]
	rules  store.Store[resource.AlertRuleSpec, resource.AlertRuleStatus]
	comps  store.Store[resource.ComponentSpec, resource.ComponentStatus]
	now    func() time.Time
}

func (h *alertsHandler) register(mux Mux) {
	mux.HandleFunc("GET /v1/alerts", h.list)
	mux.HandleFunc("GET /v1/alerts/summary", h.summary)
	mux.HandleFunc("GET /v1/alerts/{id}", h.get)
	mux.HandleFunc("POST /v1/alerts/{id}/acknowledge", h.acknowledge)
	mux.HandleFunc("POST /v1/alertrules/{id}/silence", h.silence)
	mux.HandleFunc("POST /v1/alertrules/{id}/unsilence", h.unsilence)
}

func badRequest(w http.ResponseWriter, r *http.Request, detail string) {
	WriteProblem(w, http.StatusBadRequest, "validation failed", detail, audit.RequestIDFrom(r.Context()))
}

// readableAlerts is the Alerts keep accepts that the caller may read. keep
// runs first, so authorization is decided only for the Alerts asked for.
func (h *alertsHandler) readableAlerts(w http.ResponseWriter, r *http.Request, keep func(resource.Alert) bool) ([]resource.Alert, bool) {
	all, err := h.alerts.List(r.Context(), store.ListOptions{})
	if err != nil {
		writeStoreErr(w, r, err)
		return nil, false
	}
	all = slices.DeleteFunc(all, func(a resource.Alert) bool { return !keep(a) })
	out, err := filterReadable(r.Context(), all)
	if err != nil {
		WriteProblem(w, http.StatusInternalServerError, "authorization check failed", "the list couldn't be filtered; retry later", audit.RequestIDFrom(r.Context()))
		return nil, false
	}
	return out, true
}

// list answers the caller's Alerts, newest first, filtered by
// componentId, applicationId, environmentId, state or severity, at most
// limit of them.
func (h *alertsHandler) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	state, sev := q.Get("state"), resource.Severity(q.Get("severity"))
	if state != "" && state != resource.AlertFiring && state != resource.AlertResolved {
		badRequest(w, r, "state: firing or resolved")
		return
	}
	if sev != "" && !slices.Contains([]resource.Severity{resource.SeverityInfo, resource.SeverityWarning, resource.SeverityCritical}, sev) {
		badRequest(w, r, "severity: info, warning or critical")
		return
	}
	limit := defaultAlertLimit
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxAlertLimit {
			badRequest(w, r, "limit: 1 to "+strconv.Itoa(maxAlertLimit))
			return
		}
		limit = n
	}
	comp, env := q.Get("componentId"), q.Get("environmentId")
	alerts, ok := h.readableAlerts(w, r, func(a resource.Alert) bool {
		return (comp == "" || a.Spec.ComponentID == comp) && (env == "" || a.Spec.EnvironmentID == env) &&
			(state == "" || a.Status.State == state) && (sev == "" || a.Spec.Severity == sev)
	})
	if !ok {
		return
	}
	appOf := map[string]string{}
	app := q.Get("applicationId")
	out := make([]resource.Alert, 0, len(alerts))
	for _, a := range alerts {
		if app != "" {
			got, err := h.applicationOf(r.Context(), a.Spec.ComponentID, appOf)
			if err != nil {
				writeStoreErr(w, r, err)
				return
			}
			if got != app {
				continue
			}
		}
		out = append(out, a)
	}
	slices.SortStableFunc(out, func(a, b resource.Alert) int { return b.Status.StartedAt.Compare(a.Status.StartedAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	writeJSON(w, http.StatusOK, out)
}

// applicationOf is a Component's Application, memoised in seen. A
// Component that's gone has none.
func (h *alertsHandler) applicationOf(ctx context.Context, compID string, seen map[string]string) (string, error) {
	if compID == "" {
		return "", nil
	}
	if app, ok := seen[compID]; ok {
		return app, nil
	}
	c, err := h.comps.Get(ctx, compID)
	if errors.Is(err, store.ErrNotFound) {
		seen[compID] = ""
		return "", nil
	}
	if err != nil {
		return "", err
	}
	seen[compID] = owner(c.Metadata, resource.KindApplication)
	return seen[compID], nil
}

// alertSummary is the top bar's bell: the caller's firing Alerts by
// severity.
type alertSummary struct {
	Firing         map[resource.Severity]int `json:"firing"`
	Total          int                       `json:"total"`
	Unacknowledged int                       `json:"unacknowledged"`
}

func (h *alertsHandler) summary(w http.ResponseWriter, r *http.Request) {
	alerts, ok := h.readableAlerts(w, r, func(a resource.Alert) bool { return a.Status.State == resource.AlertFiring })
	if !ok {
		return
	}
	s := alertSummary{Firing: map[resource.Severity]int{resource.SeverityCritical: 0, resource.SeverityWarning: 0, resource.SeverityInfo: 0}}
	for _, a := range alerts {
		s.Firing[a.Spec.Severity]++
		s.Total++
		if a.Status.Acknowledged == nil {
			s.Unacknowledged++
		}
	}
	writeJSON(w, http.StatusOK, s)
}

func (h *alertsHandler) get(w http.ResponseWriter, r *http.Request) {
	a, err := h.alerts.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, a)
}

// acknowledge records who acknowledged an Alert. Acknowledging it again
// changes nothing. Its audit row is the store's, with the actor and
// interface of the request.
func (h *alertsHandler) acknowledge(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	for attempt := 0; ; attempt++ {
		a, err := h.alerts.Get(ctx, r.PathValue("id"))
		if err != nil {
			writeStoreErr(w, r, err)
			return
		}
		if a.Status.Acknowledged != nil {
			writeJSON(w, http.StatusOK, a)
			return
		}
		a.Status.Acknowledged = &resource.AlertAck{By: audit.ActorFrom(ctx), At: h.now().UTC()}
		got, err := h.alerts.Update(ctx, a)
		// The evaluator and delivery loop write Alerts too: re-read on a
		// conflict.
		if errors.Is(err, store.ErrConflict) && attempt < 2 {
			continue
		}
		if err != nil {
			writeStoreErr(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, got)
		return
	}
}

// silenceRequest is POST …/silence's body.
type silenceRequest struct {
	Until  time.Time `json:"until"`
	Reason string    `json:"reason"`
}

// silence suppresses a rule's deliveries, not its Alerts, until a time
// (spec AC 8). A new silence replaces the one before.
func (h *alertsHandler) silence(w http.ResponseWriter, r *http.Request) {
	var req silenceRequest
	if !decodeBody(w, r, &req) {
		return
	}
	s := resource.AlertSilence{By: audit.ActorFrom(r.Context()), Reason: req.Reason, Until: req.Until.UTC()}
	if err := resource.ValidateSilence(s, h.now()); err != nil {
		badRequest(w, r, err.Error())
		return
	}
	h.updateRule(w, r, func(rule *resource.AlertRule) bool {
		rule.Status.Silence = &s
		return true
	})
}

// unsilence ends a rule's silence. With none it changes nothing.
func (h *alertsHandler) unsilence(w http.ResponseWriter, r *http.Request) {
	h.updateRule(w, r, func(rule *resource.AlertRule) bool {
		if rule.Status.Silence == nil {
			return false
		}
		rule.Status.Silence = nil
		return true
	})
}

// updateRule applies mutate to the stored rule's status, re-reading on a
// conflict with the evaluator's writes.
func (h *alertsHandler) updateRule(w http.ResponseWriter, r *http.Request, mutate func(*resource.AlertRule) bool) {
	ctx := r.Context()
	for attempt := 0; ; attempt++ {
		rule, err := h.rules.Get(ctx, r.PathValue("id"))
		if err != nil {
			writeStoreErr(w, r, err)
			return
		}
		if !mutate(&rule) {
			writeJSON(w, http.StatusOK, rule)
			return
		}
		got, err := h.rules.Update(ctx, rule)
		if errors.Is(err, store.ErrConflict) && attempt < 2 {
			continue
		}
		if err != nil {
			writeStoreErr(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, got)
		return
	}
}
