package notify

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/serendip-infra/serendip-infra/core/resource"
)

// SecretReader reads a Secret's value at send time (decision 8): the
// sender never caches it beyond the send.
type SecretReader func(ctx context.Context, secretID string) (string, error)

// Target is one channel to send to, with what it refers to.
type Target struct {
	Channel resource.NotificationChannelSpec
	// SMTP is the email channel's server.
	SMTP *resource.SMTPServerSpec
	// Secrets reads the channel's (and its server's) Secrets.
	Secrets SecretReader
}

// Result is a delivery's outcome. State is resource.DeliverySent or
// resource.DeliveryBelowMinSeverity; with Err set the delivery hasn't
// happened, and Err says whether to retry.
type Result struct {
	State     string
	ThreadRef string
	Err       *SendError
}

// Route is severity routing (decision 6): DeliveryBelowMinSeverity when
// sev is below the channel's minimum, so "why wasn't I told" has an
// answer, else "" (send it). A test send is always sent.
func Route(sev resource.Severity, event string, ch resource.NotificationChannelSpec) string {
	if event == EventTest || sev.Meets(ch.MinSeverity) {
		return ""
	}
	return resource.DeliveryBelowMinSeverity
}

// Deliver sends m to t. Below the channel's minimum severity nothing is
// sent and the result says so.
func (c *Client) Deliver(ctx context.Context, t Target, m Message) Result {
	ch := t.Channel
	if st := Route(m.Severity, m.Event, ch); st != "" {
		return Result{State: st}
	}
	read := func(host, id string) (string, *SendError) {
		if t.Secrets == nil {
			return "", &SendError{Host: host, Msg: "no Secrets reader"}
		}
		v, err := t.Secrets(ctx, id)
		if err != nil || v == "" {
			// The Secrets capability's error can name its own internals;
			// keep the fact, not the text.
			return "", &SendError{Host: host, Retryable: err != nil, Msg: "the Secret " + id + " couldn't be read"}
		}
		return v, nil
	}
	var ref string
	var se *SendError
	switch {
	case ch.Type == resource.ChannelTelegram && ch.Telegram != nil:
		token, e := read(telegramHost, ch.Telegram.BotTokenSecretID)
		if e != nil {
			return Result{Err: e}
		}
		ref, se = c.sendTelegram(ctx, *ch.Telegram, token, m)
	case ch.Type == resource.ChannelEmail && ch.Email != nil:
		if t.SMTP == nil {
			return Result{Err: &SendError{Host: "smtp", Msg: "the channel's SMTP server is missing"}}
		}
		for _, r := range ch.Email.To {
			if strings.ContainsAny(r, "\r\n") {
				return Result{Err: &SendError{Host: t.SMTP.Host, Msg: "a recipient holds a line break"}}
			}
		}
		user, e := read(t.SMTP.Host, t.SMTP.UsernameSecretID)
		if e != nil {
			return Result{Err: e}
		}
		pass, e := read(t.SMTP.Host, t.SMTP.PasswordSecretID)
		if e != nil {
			return Result{Err: e}
		}
		ref, se = c.sendEmail(ctx, *t.SMTP, SMTPCredentials{Username: user, Password: pass}, ch.Email.To, m)
	case ch.Type == resource.ChannelWebhook && ch.Webhook != nil:
		se = c.sendWebhook(ctx, ch, m, read)
	default:
		return Result{Err: &SendError{Host: "channel", Msg: "unknown channel type"}}
	}
	if se != nil {
		return Result{Err: se}
	}
	return Result{State: resource.DeliverySent, ThreadRef: ref}
}

// sendWebhook posts serendip.alert.v1, signed when the channel has a
// signing secret. A URL held as a Secret must still be on the host the
// channel stores, which was checked when it was saved.
func (c *Client) sendWebhook(ctx context.Context, ch resource.NotificationChannelSpec, m Message, read func(host, id string) (string, *SendError)) *SendError {
	w := ch.Webhook
	target, host := w.URL, ""
	var secrets []string
	if w.URLSecretID != "" {
		host = hostOnly(w.Host)
		v, e := read(host, w.URLSecretID)
		if e != nil {
			return e
		}
		u, err := url.Parse(v)
		if err != nil || !strings.EqualFold(u.Hostname(), host) {
			return &SendError{Host: host, Msg: "the URL's Secret points at a different host than the channel shows"}
		}
		target = v
		secrets = append(secrets, v)
	} else if u, err := url.Parse(target); err == nil {
		host = u.Hostname()
	}
	body, err := webhookBody(m)
	if err != nil {
		return &SendError{Host: host, Msg: "encoding the body failed"}
	}
	h := http.Header{"Content-Type": {"application/json"}, "X-Serendip-Event": {m.Event}}
	if w.SigningSecretID != "" {
		key, e := read(host, w.SigningSecretID)
		if e != nil {
			return e
		}
		h.Set(SignatureHeader, Sign([]byte(key), c.now(), body))
		secrets = append(secrets, key)
	}
	_, se := c.do(ctx, http.MethodPost, target, body, h, ch.AllowPrivateDestination != nil, ch.AllowPlainHTTP != nil, secrets...)
	if se != nil && w.URLSecretID != "" {
		se.Host = host
	}
	return se
}

// hostOnly drops a port and IPv6 brackets.
func hostOnly(h string) string {
	if u, err := url.Parse("//" + h); err == nil && u.Hostname() != "" {
		return u.Hostname()
	}
	return h
}
