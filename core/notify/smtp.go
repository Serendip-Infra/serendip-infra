package notify

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"time"

	"github.com/serendip-infra/serendip-infra/core/resource"
)

// SMTPCredentials are the SMTP server's username and password, read from
// their Secrets at send time and never kept beyond it (decision 8).
type SMTPCredentials struct {
	Username, Password string
}

// emailMessage renders m: a subject of "[CRITICAL] firing: <summary>", a
// plain-text and a small HTML part, and Message-ID with In-Reply-To so a
// resolution threads under its firing. It returns the message and its
// Message-ID.
func emailMessage(from *mail.Address, to []string, m Message, now time.Time) ([]byte, string) {
	domain := fromDomain(from)
	var rnd [6]byte
	_, _ = rand.Read(rnd[:])
	msgID := fmt.Sprintf("<%s.%s.%d.%s@%s>", safeToken(m.AlertID), safeToken(m.Event), now.Unix(), hex.EncodeToString(rnd[:]), domain)

	var subject string
	switch m.Event {
	case EventResolved:
		subject = "[RESOLVED] " + oneLine(m.Summary, resource.MaxAlertSummaryLen)
	case EventTest:
		subject = "[TEST] Serendip notification channel"
	default:
		subject = "[" + strings.ToUpper(string(m.Severity)) + "] firing: " + oneLine(m.Summary, resource.MaxAlertSummaryLen)
	}

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	h := textproto.MIMEHeader{}
	h.Set("Content-Type", "text/plain; charset=utf-8")
	h.Set("Content-Transfer-Encoding", "quoted-printable")
	pw, _ := mw.CreatePart(h)
	qp := quotedprintable.NewWriter(pw)
	_, _ = qp.Write([]byte(emailText(m)))
	_ = qp.Close()
	h = textproto.MIMEHeader{}
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Transfer-Encoding", "quoted-printable")
	pw, _ = mw.CreatePart(h)
	qp = quotedprintable.NewWriter(pw)
	_, _ = qp.Write([]byte(emailHTML(m)))
	_ = qp.Close()
	_ = mw.Close()

	var out bytes.Buffer
	hdr := func(k, v string) { out.WriteString(k + ": " + v + "\r\n") }
	hdr("From", from.String())
	hdr("To", strings.Join(to, ", "))
	hdr("Subject", mime.QEncoding.Encode("utf-8", subject))
	hdr("Date", now.UTC().Format(time.RFC1123Z))
	hdr("Message-ID", msgID)
	if ref := m.ThreadRef; ref != "" && validMsgID(ref) {
		hdr("In-Reply-To", ref)
		hdr("References", ref)
	}
	hdr("Auto-Submitted", "auto-generated")
	hdr("MIME-Version", "1.0")
	hdr("Content-Type", "multipart/alternative; boundary="+mw.Boundary())
	out.WriteString("\r\n")
	out.Write(body.Bytes())
	return out.Bytes(), msgID
}

// fromDomain is the sender address's domain, for the Message-ID and
// the HELO name (some relays refuse a name that isn't a domain).
func fromDomain(from *mail.Address) string {
	if i := strings.LastIndexByte(from.Address, '@'); i >= 0 && i < len(from.Address)-1 {
		return from.Address[i+1:]
	}
	return "localhost"
}

func safeToken(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 128 && (r == '-' || r == '_' || r == '.' || ('0' <= r && r <= '9') || ('a' <= r && r <= 'z') || ('A' <= r && r <= 'Z')) {
			return r
		}
		return -1
	}, s)
	if s == "" {
		return "x"
	}
	return s
}

// validMsgID is a Message-ID with no room to add a header.
func validMsgID(s string) bool {
	return len(s) < 250 && strings.HasPrefix(s, "<") && strings.HasSuffix(s, ">") && !strings.ContainsAny(s, "\r\n ")
}

func emailLines(m Message) []string {
	var lines []string
	add := func(k, v string) {
		if v != "" {
			lines = append(lines, k+": "+oneLine(v, 300))
		}
	}
	switch m.Event {
	case EventTest:
		return []string{"A test notification from Serendip: this channel works."}
	case EventResolved:
		lines = append(lines, "Resolved: "+oneLine(m.Summary, resource.MaxAlertSummaryLen))
	default:
		lines = append(lines, strings.ToUpper(string(m.Severity))+" firing: "+oneLine(m.Summary, resource.MaxAlertSummaryLen))
	}
	add("Condition", m.Detail)
	add("Component", m.Component)
	add("Application", m.Application)
	add("Environment", m.Environment)
	add("Project", m.Project)
	add("Rule", m.Rule)
	if !m.StartedAt.IsZero() {
		add("Started", m.StartedAt.UTC().Format(time.RFC3339))
	}
	if m.ResolvedAt != nil {
		add("Resolved", m.ResolvedAt.UTC().Format(time.RFC3339))
	}
	return lines
}

func emailText(m Message) string {
	s := strings.Join(emailLines(m), "\r\n")
	if m.ConsoleURL != "" {
		s += "\r\n\r\nView in console: " + oneLine(m.ConsoleURL, 500)
	}
	return s + "\r\n"
}

func emailHTML(m Message) string {
	var b strings.Builder
	b.WriteString("<!doctype html><html><body style=\"font-family:sans-serif\">")
	for i, l := range emailLines(m) {
		if i == 0 {
			b.WriteString("<p><b>" + escapeHTML(l) + "</b></p><p>")
			continue
		}
		b.WriteString(escapeHTML(l) + "<br>")
	}
	b.WriteString("</p>")
	if u := m.ConsoleURL; u != "" && (strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://")) {
		b.WriteString(`<p><a href="` + attrEscaper.Replace(u) + `">View in console</a></p>`)
	}
	b.WriteString("</body></html>")
	return b.String()
}

// smtpError classifies an SMTP answer: 4xx is temporary and retried, 5xx
// is the server refusing.
func smtpError(host string, err error, secrets ...string) *SendError {
	if te, ok := errors.AsType[*textproto.Error](err); ok {
		return &SendError{Host: host, Status: te.Code, Retryable: te.Code >= 400 && te.Code < 500, Msg: scrub(oneLine(te.Msg, 200), secrets...)}
	}
	return clean(host, err, secrets...)
}

// sendEmail sends m through srv (decision 5): STARTTLS is required on a
// starttls server, or the connection is TLS from the start; credentials
// go only over TLS, so a server offering AUTH before STARTTLS (T001's
// Brevo on 587) never sees them in clear.
func (c *Client) sendEmail(ctx context.Context, srv resource.SMTPServerSpec, cred SMTPCredentials, to []string, m Message) (string, *SendError) {
	host := srv.Host
	secrets := []string{cred.Password}
	from, err := mail.ParseAddress(srv.From)
	if err != nil || strings.ContainsAny(srv.From, "\r\n") {
		return "", &SendError{Host: host, Msg: "the server's from address isn't an address"}
	}
	for _, r := range to {
		if a, err := mail.ParseAddress(r); err != nil || a.Address != r {
			return "", &SendError{Host: host, Msg: "a recipient isn't a bare address"}
		}
	}
	msg, msgID := emailMessage(from, to, m, c.now())
	ctx, cancel := context.WithTimeout(ctx, smtpTimeout)
	defer cancel()
	// An SMTP server has no private-destination allowance (plan decision
	// 1): it's a relay on the internet (Gmail, Brevo), and a mail server
	// on the LAN is out of scope for now.
	conn, err := c.dial.DialContext(withAllowPrivate(ctx, false), "tcp", net.JoinHostPort(host, strconv.Itoa(srv.Port)))
	if err != nil {
		se := clean(host, err, secrets...)
		if se.Refused() {
			se.Msg += "; an SMTP server must be on a public address"
		}
		return "", se
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	tlsCfg := c.tls.Clone()
	tlsCfg.ServerName = host
	if srv.TLS == resource.SMTPImplicit {
		tc := tls.Client(conn, tlsCfg)
		if err := tc.HandshakeContext(ctx); err != nil {
			return "", clean(host, err, secrets...)
		}
		conn = tc
	} else if srv.TLS != resource.SMTPStartTLS {
		return "", &SendError{Host: host, Msg: "plain SMTP isn't offered"}
	}
	sc, err := smtp.NewClient(conn, host)
	if err != nil {
		return "", smtpError(host, err, secrets...)
	}
	defer sc.Close()
	if err := sc.Hello(fromDomain(from)); err != nil {
		return "", smtpError(host, err, secrets...)
	}
	if srv.TLS == resource.SMTPStartTLS {
		if ok, _ := sc.Extension("STARTTLS"); !ok {
			return "", &SendError{Host: host, Msg: "the server doesn't offer STARTTLS; credentials are sent only over TLS"}
		}
		if err := sc.StartTLS(tlsCfg); err != nil {
			return "", smtpError(host, err, secrets...)
		}
	}
	if state, ok := sc.TLSConnectionState(); !ok || !state.HandshakeComplete {
		return "", &SendError{Host: host, Msg: "not on TLS; credentials are sent only over TLS"}
	}
	if ok, _ := sc.Extension("AUTH"); !ok {
		return "", &SendError{Host: host, Msg: "the server offers no AUTH after TLS"}
	}
	if err := sc.Auth(smtp.PlainAuth("", cred.Username, cred.Password, host)); err != nil {
		return "", smtpError(host, err, secrets...)
	}
	if err := sc.Mail(from.Address); err != nil {
		return "", smtpError(host, err, secrets...)
	}
	for _, r := range to {
		if err := sc.Rcpt(r); err != nil {
			return "", smtpError(host, err, secrets...)
		}
	}
	w, err := sc.Data()
	if err != nil {
		return "", smtpError(host, err, secrets...)
	}
	if _, err := w.Write(msg); err != nil {
		return "", smtpError(host, err, secrets...)
	}
	if err := w.Close(); err != nil {
		return "", smtpError(host, err, secrets...)
	}
	_ = sc.Quit()
	return msgID, nil
}
