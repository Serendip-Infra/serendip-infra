package notify

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Limits on a send (decision 5).
const (
	sendTimeout     = 10 * time.Second
	smtpTimeout     = 30 * time.Second
	maxResponseBody = 64 << 10
	// MaxRetryAfter caps how long a receiver's Retry-After is honoured.
	MaxRetryAfter = 15 * time.Minute
	userAgent     = "serendip-notify/1"
)

// Options configure a Client. The zero value is production's.
type Options struct {
	// Lookup resolves names; nil uses the system resolver.
	Lookup Lookup
	// RootCAs verifies servers; nil uses the system's roots.
	RootCAs *x509.CertPool
}

// Client is the one sender every channel and the heartbeat use: checked
// dialer, no proxy, no redirects, TLS 1.2 or later, a 10 s timeout and
// a 64 KiB cap on what it reads back.
type Client struct {
	Check *Checker
	dial  *dialer
	http  *http.Client
	tls   *tls.Config
	// telegramBase is the Bot API's address; fixed outside tests.
	telegramBase string
	now          func() time.Time
}

// NewClient returns a Client with its own Checker.
func NewClient(opts Options) *Client {
	check := &Checker{}
	lookup := opts.Lookup
	if lookup == nil {
		lookup = defaultLookup
	}
	d := &dialer{check: check, lookup: lookup, timeout: sendTimeout}
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: opts.RootCAs}
	tr := &http.Transport{
		Proxy:                 nil, // HTTPS_PROXY must not route around the check
		DialContext:           d.DialContext,
		TLSClientConfig:       tlsCfg,
		TLSHandshakeTimeout:   sendTimeout,
		ResponseHeaderTimeout: sendTimeout,
		// No pooling: an idle connection opened under one channel's
		// private allowance must never carry another channel's send,
		// and a pooled connection would skip the dial-time check. Sends
		// are rare, so a fresh connection each time costs little.
		DisableKeepAlives: true,
		ForceAttemptHTTP2: false,
	}
	return &Client{
		Check: check,
		dial:  d,
		http: &http.Client{
			Transport: tr,
			Timeout:   sendTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		tls:          tlsCfg,
		telegramBase: "https://api.telegram.org",
		now:          time.Now,
	}
}

// SendError is a failed send, cleaned for storing on an Alert, an event
// or a log: it names the host, never a URL path, a query or a token
// (decision 8).
type SendError struct {
	Host string
	// Status is the receiver's HTTP or SMTP status, 0 when none came back.
	Status int
	// Retryable: a network error, a timeout, 408, 429 or a 5xx (or an
	// SMTP 4xx). Anything else is the receiver refusing the request.
	Retryable  bool
	RetryAfter time.Duration
	Msg        string
	// refused: the address check refused the destination.
	refused bool
	// body is the receiver's answer, read by the channel that knows its
	// format (Telegram's retry_after); never shown.
	body []byte
}

func (e *SendError) Error() string {
	if e.Status != 0 {
		return fmt.Sprintf("%s: %d %s", e.Host, e.Status, e.Msg)
	}
	return e.Host + ": " + e.Msg
}

// Refused says whether the address check refused the destination.
func (e *SendError) Refused() bool { return e.refused }

// clean turns err into a SendError naming only host, with each secret
// scrubbed from the text as a second guard.
func clean(host string, err error, secrets ...string) *SendError {
	if se, ok := errors.AsType[*SendError](err); ok {
		return se
	}
	// A url.Error's message carries the whole URL; keep what it wraps.
	for {
		ue, ok := errors.AsType[*url.Error](err)
		if !ok {
			break
		}
		err = ue.Err
	}
	se := &SendError{Host: host, Msg: err.Error()}
	switch {
	case errors.Is(err, ErrRefusedAddress):
		se.refused = true
	case errors.Is(err, context.Canceled):
		// serendipd stopping: not the receiver's failure, and not a
		// reason to count an attempt. The caller leaves it pending.
		se.Msg = "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		se.Msg, se.Retryable = "timed out", true
	default:
		if de, ok := errors.AsType[*dnsError](err); ok {
			// A timeout or a failing server is retried; an unknown name
			// isn't.
			se.Retryable = de.temporary
			break
		}
		var ne net.Error
		if errors.As(err, &ne) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
			se.Retryable = true
		}
		if _, ok := errors.AsType[*net.OpError](err); ok {
			se.Retryable = true
		}
	}
	se.Msg = scrub(se.Msg, secrets...)
	return se
}

// scrub removes every secret (and its URL-escaped form) from s.
func scrub(s string, secrets ...string) string {
	for _, v := range secrets {
		if len(v) < 4 {
			continue
		}
		s = strings.ReplaceAll(s, v, "[redacted]")
		if q := url.PathEscape(v); q != v {
			s = strings.ReplaceAll(s, q, "[redacted]")
		}
	}
	return s
}

// statusError classifies a non-2xx answer (decision 7): 408, 429 and 5xx
// are retried, other 4xx aren't, and a redirect is refused.
func statusError(host string, resp *http.Response, now time.Time) *SendError {
	se := &SendError{Host: host, Status: resp.StatusCode, Msg: http.StatusText(resp.StatusCode)}
	switch {
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		se.Msg = "redirect refused"
	case resp.StatusCode == http.StatusRequestTimeout, resp.StatusCode >= 500:
		se.Retryable = true
	case resp.StatusCode == http.StatusTooManyRequests:
		se.Retryable = true
		se.RetryAfter = parseRetryAfter(resp.Header.Get("Retry-After"), now)
	}
	return se
}

// parseRetryAfter reads seconds or an HTTP date, capped at MaxRetryAfter.
func parseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	var d time.Duration
	if n, err := strconv.Atoi(v); err == nil {
		d = time.Duration(n) * time.Second
	} else if t, err := http.ParseTime(v); err == nil {
		d = t.Sub(now)
	}
	return capRetry(d)
}

func capRetry(d time.Duration) time.Duration {
	switch {
	case d < 0:
		return 0
	case d > MaxRetryAfter:
		return MaxRetryAfter
	}
	return d
}

// do sends one request through the checked client. plainHTTP permits an
// http URL (the channel's allowance); allowPrivate its private-range
// allowance. It returns the 2xx body, capped.
func (c *Client) do(ctx context.Context, method, rawURL string, body []byte, header http.Header, allowPrivate, plainHTTP bool, secrets ...string) ([]byte, *SendError) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return nil, &SendError{Host: "(invalid URL)", Msg: "not a URL"}
	}
	host := u.Hostname()
	if u.Scheme != "https" && !(u.Scheme == "http" && plainHTTP) {
		return nil, &SendError{Host: host, Msg: "HTTPS only, unless plain HTTP is allowed for this channel"}
	}
	if u.User != nil {
		return nil, &SendError{Host: host, Msg: "credentials in the URL are refused"}
	}
	req, err := http.NewRequestWithContext(withAllowPrivate(ctx, allowPrivate), method, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, clean(host, err, slices.Concat(secrets, []string{rawURL})...)
	}
	for k, vs := range header {
		req.Header[k] = vs
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, clean(host, err, slices.Concat(secrets, []string{rawURL, u.Path, u.RawQuery})...)
	}
	defer resp.Body.Close()
	// A 2xx answer over 64 KiB is cut short: the only body read is
	// Telegram's small result, and the rest is never needed.
	data, rerr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		se := statusError(host, resp, c.now())
		se.body = data
		return nil, se
	}
	if rerr != nil {
		return nil, clean(host, rerr, slices.Concat(secrets, []string{rawURL})...)
	}
	return data, nil
}

// CheckURL is the create-, update- and test-time check of a webhook or
// heartbeat URL: its shape, and every address its host resolves to.
func (c *Client) CheckURL(ctx context.Context, rawURL string, allowPrivate, plainHTTP bool) error {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return errors.New("not a URL")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && plainHTTP) {
		return errors.New("HTTPS only, unless an Owner allows plain HTTP for this channel")
	}
	return c.CheckHost(ctx, u.Hostname(), allowPrivate)
}

// CheckHost checks every address host resolves to. host may carry a
// port, which is ignored.
func (c *Client) CheckHost(ctx context.Context, host string, allowPrivate bool) error {
	if _, err := netip.ParseAddr(host); err != nil {
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		} else if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
			host = host[1 : len(host)-1]
		}
	}
	_, err := c.dial.resolve(ctx, host, allowPrivate)
	return err
}
