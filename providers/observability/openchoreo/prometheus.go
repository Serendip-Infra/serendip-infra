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

// The Prometheus query client (specs/024-alerts plan decision 3): the
// query paths the adapters don't cover, kube-state-metrics' restart
// counter and (spec 025) Hubble's request metrics. It's Prometheus' HTTP API v1 instant query (VERSIONS.md),
// through the same Service proxy as the stores' /metrics, with
// serendipd's own credentials. Only fixed templates filled from
// validated IDs are sent (conditions.go), never text from a person.

// promResponse is /api/v1/query's and /api/v1/query_range's answer.
type promResponse struct {
	Status    string `json:"status"`
	ErrorType string `json:"errorType"`
	Data      struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
			// Value is [unix seconds, "value"] (a vector).
			Value [2]any `json:"value"`
			// Values are Value's, oldest first (a matrix).
			Values [][2]any `json:"values"`
		} `json:"result"`
	} `json:"data"`
}

// errStoreUnreachable: the metrics store's Service isn't there.
var errStoreUnreachable = errors.New("the metrics store isn't reachable")

// sample is one labelled sample of a vector.
type sample struct {
	labels map[string]string
	value  float64
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
	ss, err := p.vector(ctx, query, at)
	if err != nil {
		return 0, false, err
	}
	switch len(ss) {
	case 0:
		return 0, false, nil
	case 1:
	default:
		return 0, false, fmt.Errorf("openchoreo observability: the metrics store answered %d samples for a sum", len(ss))
	}
	v = ss[0].value
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false, reconcile.ErrNoData
	}
	return v, true, nil
}

// vector runs query at at, and returns every sample with its labels.
// Non-finite values are kept: the caller says what they mean.
func (p prometheus) vector(ctx context.Context, query string, at time.Time) ([]sample, error) {
	q := url.Values{}
	q.Set("query", query)
	q.Set("time", promTime(at))
	out, err := p.get(ctx, "/api/v1/query?"+q.Encode(), "vector")
	if err != nil {
		return nil, err
	}
	ss := make([]sample, 0, len(out.Data.Result))
	for _, r := range out.Data.Result {
		v, err := promValue(r.Value)
		if err != nil {
			return nil, err
		}
		ss = append(ss, sample{labels: r.Metric, value: v})
	}
	return ss, nil
}

// series runs query over [from, to] at step, and returns its one series,
// oldest first, without non-finite points (a quantile over no requests is
// NaN). No series is an empty answer.
func (p prometheus) series(ctx context.Context, query string, from, to time.Time, step time.Duration) ([]reconcile.MetricPoint, error) {
	q := url.Values{}
	q.Set("query", query)
	q.Set("start", promTime(from))
	q.Set("end", promTime(to))
	q.Set("step", strconv.Itoa(int(step/time.Second)))
	out, err := p.get(ctx, "/api/v1/query_range?"+q.Encode(), "matrix")
	if err != nil {
		return nil, err
	}
	pts := []reconcile.MetricPoint{}
	switch len(out.Data.Result) {
	case 0:
		return pts, nil
	case 1:
	default:
		return nil, fmt.Errorf("openchoreo observability: the metrics store answered %d series for a sum", len(out.Data.Result))
	}
	for _, raw := range out.Data.Result[0].Values {
		v, err := promValue(raw)
		if err != nil {
			return nil, err
		}
		secs, isNum := raw[0].(float64)
		if !isNum {
			return nil, errors.New("openchoreo observability: the metrics store's point has no time")
		}
		if math.IsNaN(v) || math.IsInf(v, 0) {
			continue
		}
		pts = append(pts, reconcile.MetricPoint{Time: time.UnixMilli(int64(math.Round(secs * 1000))).UTC(), Value: v})
	}
	if len(pts) > reconcile.MaxMetricPoints {
		pts = pts[len(pts)-reconcile.MaxMetricPoints:]
	}
	return pts, nil
}

// get sends one query and checks the answer is of kind.
func (p prometheus) get(ctx context.Context, pathAndQuery, kind string) (promResponse, error) {
	path := kubernetes.ServiceProxyPath(PlaneNamespace, prometheusService, prometheusPort) + pathAndQuery
	raw, err := p.client.GetRaw(ctx, path)
	if err != nil {
		var se *kubernetes.StatusError
		if errors.As(err, &se) {
			// Not the body: it echoes the query.
			return promResponse{}, fmt.Errorf("openchoreo observability: the metrics store query failed with status %d", se.Code)
		}
		if errors.Is(err, kubernetes.ErrNotFound) {
			return promResponse{}, fmt.Errorf("openchoreo observability: %w (not found)", errStoreUnreachable)
		}
		return promResponse{}, fmt.Errorf("openchoreo observability: metrics store query: %w", err)
	}
	var out promResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return promResponse{}, fmt.Errorf("openchoreo observability: decode the metrics store answer: %w", err)
	}
	if out.Status != "success" {
		return promResponse{}, fmt.Errorf("openchoreo observability: the metrics store refused the query (%s)", out.ErrorType)
	}
	if out.Data.ResultType != kind {
		return promResponse{}, fmt.Errorf("openchoreo observability: the metrics store answered a %q, not a %s", out.Data.ResultType, kind)
	}
	return out, nil
}

// promValue reads a sample's [time, "value"].
func promValue(raw [2]any) (float64, error) {
	s, isString := raw[1].(string)
	if !isString {
		return 0, errors.New("openchoreo observability: the metrics store's sample has no value")
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("openchoreo observability: the metrics store's sample %q: %w", s, err)
	}
	return v, nil
}

// promTime is t as the API's unix seconds, to the millisecond.
func promTime(t time.Time) string {
	return strconv.FormatFloat(float64(t.UnixMilli())/1000, 'f', 3, 64)
}
