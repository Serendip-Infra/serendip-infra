package reconcile

import (
	"context"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/serendip-infra/serendip-infra/core/resource"
)

// The observability capability (specs/023-observability plan decision 5):
// logs and metrics kept after the pods that produced them are gone. It
// speaks in Serendip's terms; no backend's names appear here
// (constitution 1). It's registered once its package is Ready, and
// returns ErrProviderNotReady until then.

// Caps on observability queries (plan decisions 5, 6, 7 and 11).
const (
	// MaxLogQueryLimit is the most lines one page returns.
	MaxLogQueryLimit = 500
	// MaxLogTargets is the most Deployments one search reads.
	MaxLogTargets = 20
	// MaxLogLineBytes is where a longer stored line is cut.
	MaxLogLineBytes = 16 << 10
	// MaxMetricPoints is the most points in one series.
	MaxMetricPoints = 300
)

// LogQuery selects stored log lines, newest first. The API layer
// validates and caps it before a provider sees it; a provider may still
// clamp Limit to MaxLogQueryLimit.
type LogQuery struct {
	From time.Time
	// To zero means now.
	To time.Time
	// Text is a plain phrase to search for, not a query language.
	Text string
	// Limit is capped at MaxLogQueryLimit.
	Limit int
	// Before is the opaque cursor from the previous page's Next.
	Before string
}

// LogTarget is one Component's logs in one Environment. All three are
// passed so a provider scopes by each, and never widens a query.
type LogTarget struct {
	Deployment  resource.Deployment
	Component   resource.Component
	Environment resource.Environment
}

// StoredLogLine is one stored line.
type StoredLogLine struct {
	Time time.Time `json:"time"`
	// Component is the Component's ID.
	Component string `json:"component"`
	// Instance is the running copy that wrote it.
	Instance  string `json:"instance"`
	Container string `json:"container,omitempty"`
	Line      string `json:"line"`
}

// LogPage is one page of stored lines, newest first. Next is the cursor
// for the next, older page, empty when there's none: a page can be short,
// even empty, with Next set, so only an empty Next ends a search.
// Truncated says a line was cut or a cap was reached.
type LogPage struct {
	Lines     []StoredLogLine `json:"lines"`
	Next      string          `json:"next,omitempty"`
	Truncated bool            `json:"truncated,omitempty"`
}

// MetricsQuery selects a range of metrics. Step zero means the provider
// chooses one giving at most MaxMetricPoints points.
type MetricsQuery struct {
	From, To time.Time
	Step     time.Duration
}

// MetricPoint is one sample.
type MetricPoint struct {
	Time  time.Time `json:"time"`
	Value float64   `json:"value"`
}

// Metrics is a Deployment's CPU (millicores) and memory (MiB), with its
// requests and limits where known. Unavailable names what couldn't be
// read, each with its reason, such as "requests: not set".
type Metrics struct {
	CPU           []MetricPoint `json:"cpu"`
	Memory        []MetricPoint `json:"memory"`
	CPURequest    *float64      `json:"cpuRequest,omitempty"`
	CPULimit      *float64      `json:"cpuLimit,omitempty"`
	MemoryRequest *float64      `json:"memoryRequest,omitempty"`
	MemoryLimit   *float64      `json:"memoryLimit,omitempty"`
	Unavailable   []string      `json:"unavailable,omitempty"`
}

// ObservabilityProvider reads stored logs and metrics. Callers scope
// before calling: a provider reads only the targets it's given.
type ObservabilityProvider interface {
	SearchLogs(ctx context.Context, targets []LogTarget, q LogQuery) (LogPage, error)
	// BuildLogs reads a Build's stored lines, for once its pods are gone.
	BuildLogs(ctx context.Context, b resource.Build, q LogQuery) (LogPage, error)
	Metrics(ctx context.Context, t LogTarget, q MetricsQuery) (Metrics, error)
}

// LogCursor is where the next page of a time-ordered, newest-first search
// starts: before Time, after skipping Seen lines at exactly Time (lines
// can share a timestamp, so time alone would repeat or drop them).
type LogCursor struct {
	Time time.Time
	Seen int
}

// ErrBadCursor is returned for a cursor this package didn't make.
var ErrBadCursor = errors.New("reconcile: bad log cursor")

// cursorVersion lets the encoding change without old cursors being
// misread.
const cursorVersion = "1"

// maxCursorSeen bounds Seen in a cursor from outside, so a forged one
// can't ask a provider to skip without end.
const maxCursorSeen = 1 << 20

// Encode returns c as an opaque, URL-safe string. The zero LogCursor (and
// any zero time) encodes as "", the first page.
func (c LogCursor) Encode() string {
	if c.Time.IsZero() {
		return ""
	}
	raw := cursorVersion + ":" + strconv.FormatInt(c.Time.UnixNano(), 10) + ":" + strconv.Itoa(c.Seen)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// DecodeLogCursor reads a cursor made by Encode, refusing any other
// spelling of it. The empty string is the first page: the zero LogCursor
// and no error.
func DecodeLogCursor(s string) (LogCursor, error) {
	if s == "" {
		return LogCursor{}, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return LogCursor{}, ErrBadCursor
	}
	parts := strings.Split(string(raw), ":")
	if len(parts) != 3 || parts[0] != cursorVersion {
		return LogCursor{}, ErrBadCursor
	}
	ns, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || ns <= 0 {
		return LogCursor{}, ErrBadCursor
	}
	seen, err := strconv.Atoi(parts[2])
	if err != nil || seen < 0 || seen > maxCursorSeen {
		return LogCursor{}, ErrBadCursor
	}
	c := LogCursor{Time: time.Unix(0, ns).UTC(), Seen: seen}
	if c.Encode() != s {
		return LogCursor{}, ErrBadCursor
	}
	return c, nil
}
