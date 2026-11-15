package openchoreo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"time"

	"github.com/serendip-infra/serendip-infra/core/reconcile"
	"github.com/serendip-infra/serendip-infra/providers/kubernetes"
)

// The Prometheus query client (specs/024-alerts plan decision 3): the one
// query path the adapters don't cover, kube-state-metrics' restart
// counter. It's Prometheus' HTTP API v1 instant query (VERSIONS.md),
// through the same Service proxy as the stores' /metrics, with
// serendipd's own credentials. Only fixed templates filled from
// validated IDs are sent (conditions.go), never text from a person.

// promResponse is /api/v1/query's answer. Only vectors are read: every
// query this package sends is a sum, so at most one sample comes back.
type promResponse struct {
	Status    string `json:"status"`
	ErrorType string `json:"errorType"`
	Data      struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			// Value is [unix seconds, "value"].
			Value [2]any `json:"value"`
		} `json:"result"`
	} `json:"data"`
}

// prometheus queries the metrics store.
type prometheus struct {
	client *kubernetes.Client
}

// exists reports whether query, a count, matched anything at at.
func (p prometheus) exists(ctx context.Context, query string, at time.Time) (bool, error) {
	v, ok, err := p.instant(ctx, query, at)
	if errors.Is(err, reconcile.ErrNoData) {
		return false, nil
	}
	return ok && v > 0, err
}

// instant runs query at at, and returns its one sample's value. An empty
// vector is ok false: nothing matched.
func (p prometheus) instant(ctx context.Context, query string, at time.Time) (v float64, ok bool, err error) {
	q := url.Values{}
	q.Set("query", query)
	q.Set("time", strconv.FormatFloat(float64(at.UnixMilli())/1000, 'f', 3, 64))
	path := kubernetes.ServiceProxyPath(PlaneNamespace, prometheusService, prometheusPort) + "/api/v1/query?" + q.Encode()
	raw, err := p.client.GetRaw(ctx, path)
	if err != nil {
		var se *kubernetes.StatusError
		if errors.As(err, &se) {
			// Not the body: it echoes the query.
			return 0, false, fmt.Errorf("openchoreo observability: the metrics store query failed with status %d", se.Code)
		}
		if errors.Is(err, kubernetes.ErrNotFound) {
			return 0, false, errors.New("openchoreo observability: the metrics store isn't reachable (not found)")
		}
		return 0, false, fmt.Errorf("openchoreo observability: metrics store query: %w", err)
	}
	var out promResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return 0, false, fmt.Errorf("openchoreo observability: decode the metrics store answer: %w", err)
	}
	if out.Status != "success" {
		return 0, false, fmt.Errorf("openchoreo observability: the metrics store refused the query (%s)", out.ErrorType)
	}
	if out.Data.ResultType != "vector" {
		return 0, false, fmt.Errorf("openchoreo observability: the metrics store answered a %q, not a vector", out.Data.ResultType)
	}
	switch len(out.Data.Result) {
	case 0:
		return 0, false, nil
	case 1:
	default:
		return 0, false, fmt.Errorf("openchoreo observability: the metrics store answered %d samples for a sum", len(out.Data.Result))
	}
	s, isString := out.Data.Result[0].Value[1].(string)
	if !isString {
		return 0, false, errors.New("openchoreo observability: the metrics store's sample has no value")
	}
	v, err = strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false, fmt.Errorf("openchoreo observability: the metrics store's sample %q: %w", s, err)
	}
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false, reconcile.ErrNoData
	}
	return v, true, nil
}
