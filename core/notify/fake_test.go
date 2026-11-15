package notify_test

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/textproto"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/serendip-infra/serendip-infra/core/notify"
)

// testCert is one self-signed certificate for every fake server.
func testCert(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "notify test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		DNSNames:              []string{"hooks.example.com", "smtp.example.com", "tg.example.com", "hc.example.com", "other.example.com"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, pool
}

// env is a client whose test names resolve to loopback, which the check
// lets through for 127.0.0.1 only.
type env struct {
	t      *testing.T
	cert   tls.Certificate
	client *notify.Client
	mu     sync.Mutex
	names  map[string][]netip.Addr
}

func newEnv(t *testing.T) *env {
	cert, pool := testCert(t)
	e := &env{t: t, cert: cert, names: map[string][]netip.Addr{}}
	for _, n := range []string{"hooks.example.com", "smtp.example.com", "tg.example.com", "hc.example.com"} {
		e.names[n] = []netip.Addr{netip.MustParseAddr("127.0.0.1")}
	}
	e.client = notify.NewClient(notify.Options{RootCAs: pool, Lookup: func(_ context.Context, host string) ([]netip.Addr, error) {
		e.mu.Lock()
		defer e.mu.Unlock()
		if a, ok := e.names[host]; ok {
			return a, nil
		}
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}})
	e.client.Check.Exempt(netip.MustParsePrefix("127.0.0.1/32"))
	return e
}

func (e *env) resolve(host string, addrs ...string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.names[host] = nil
	for _, a := range addrs {
		e.names[host] = append(e.names[host], netip.MustParseAddr(a))
	}
}

// server is an HTTPS server with the test certificate. It returns its
// port.
func (e *env) server(h http.Handler) string {
	s := httptest.NewUnstartedServer(h)
	s.TLS = &tls.Config{Certificates: []tls.Certificate{e.cert}}
	s.StartTLS()
	e.t.Cleanup(s.Close)
	_, port, _ := net.SplitHostPort(s.Listener.Addr().String())
	return port
}

// fakeSMTP is a minimal SMTP server that records what it was told, and
// in what order.
type fakeSMTP struct {
	implicit      bool // TLS from the start (port 465)
	offerSTARTTLS bool
	authBeforeTLS bool // T001: Brevo on 587 offers AUTH before STARTTLS
	cert          tls.Certificate
	mu            sync.Mutex
	steps         []string // e.g. "EHLO", "STARTTLS", "AUTH(tls)", "AUTH(plain)"
	authUser      string
	authPass      string
	data          []string
	port          string
}

func (f *fakeSMTP) start(t *testing.T) {
	var ln net.Listener
	var err error
	cfg := &tls.Config{Certificates: []tls.Certificate{f.cert}}
	if f.implicit {
		ln, err = tls.Listen("tcp", "127.0.0.1:0", cfg)
	} else {
		ln, err = net.Listen("tcp", "127.0.0.1:0")
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	_, f.port, _ = net.SplitHostPort(ln.Addr().String())
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c, cfg)
		}
	}()
}

func (f *fakeSMTP) step(s string) {
	f.mu.Lock()
	f.steps = append(f.steps, s)
	f.mu.Unlock()
}

func (f *fakeSMTP) serve(c net.Conn, cfg *tls.Config) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	onTLS := f.implicit
	tp := textproto.NewConn(c)
	_ = tp.PrintfLine("220 fake ESMTP")
	for {
		line, err := tp.ReadLine()
		if err != nil {
			return
		}
		verb := strings.ToUpper(strings.Fields(line + " x")[0])
		switch verb {
		case "EHLO", "HELO":
			f.step("EHLO")
			ext := []string{"fake"}
			if f.offerSTARTTLS && !onTLS {
				ext = append(ext, "STARTTLS")
			}
			if onTLS || f.authBeforeTLS {
				ext = append(ext, "AUTH PLAIN LOGIN")
			}
			for i, x := range ext {
				sep := "-"
				if i == len(ext)-1 {
					sep = " "
				}
				_ = tp.PrintfLine("250%s%s", sep, x)
			}
		case "STARTTLS":
			f.step("STARTTLS")
			_ = tp.PrintfLine("220 go ahead")
			tc := tls.Server(c, cfg)
			if tc.Handshake() != nil {
				return
			}
			c, onTLS = tc, true
			tp = textproto.NewConn(tc)
		case "AUTH":
			mode := "plain"
			if onTLS {
				mode = "tls"
			}
			f.step("AUTH(" + mode + ")")
			parts := strings.Fields(line)
			if len(parts) == 3 {
				raw, _ := base64.StdEncoding.DecodeString(parts[2])
				fs := strings.Split(string(raw), "\x00")
				if len(fs) == 3 {
					f.mu.Lock()
					f.authUser, f.authPass = fs[1], fs[2]
					f.mu.Unlock()
				}
			}
			_ = tp.PrintfLine("235 ok")
		case "MAIL", "RCPT":
			_ = tp.PrintfLine("250 ok")
		case "DATA":
			_ = tp.PrintfLine("354 go")
			b, err := bufio.NewReader(tp.DotReader()).ReadString(0)
			if err != nil && b == "" {
				return
			}
			f.mu.Lock()
			f.data = append(f.data, b)
			f.mu.Unlock()
			_ = tp.PrintfLine("250 queued")
		case "QUIT":
			_ = tp.PrintfLine("221 bye")
			return
		default:
			_ = tp.PrintfLine("502 no")
		}
	}
}
