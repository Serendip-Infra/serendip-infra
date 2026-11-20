package notify

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"slices"
	"testing"
	"time"
)

// dualStack is a name resolving to two IPv6 addresses and one IPv4.
var dualStack = []netip.Addr{netip.MustParseAddr("2606:4700::1"), netip.MustParseAddr("2606:4700::2"), netip.MustParseAddr("1.1.1.1")}

// stubDialer dials nothing: connect runs nd's Control hook on the
// address (as a real dial would), then answers with result(addr).
func stubDialer(result func(addr string) error, tried *[]string, shares *[]time.Duration) *dialer {
	return &dialer{
		check:   &Checker{},
		timeout: 10 * time.Second,
		lookup:  func(context.Context, string) ([]netip.Addr, error) { return dualStack, nil },
		connect: func(ctx context.Context, nd *net.Dialer, _, addr string) (net.Conn, error) {
			*tried = append(*tried, addr)
			if shares != nil {
				*shares = append(*shares, nd.Timeout)
			}
			if err := nd.Control("tcp", addr, nil); err != nil {
				return nil, err
			}
			if err := result(addr); err != nil {
				return nil, err
			}
			c, _ := net.Pipe()
			return c, nil
		},
	}
}

var errTimeout = &net.OpError{Op: "dial", Err: errors.New("i/o timeout")}

// TestDialSkipsHangingAddress: one unreachable address (a broken IPv6
// route) mustn't use up the whole send before the next one is tried
// (spec 024 T010 F1, found live against a dual-stack name).
func TestDialSkipsHangingAddress(t *testing.T) {
	var tried []string
	var shares []time.Duration
	d := stubDialer(func(addr string) error {
		if addr == "1.1.1.1:443" {
			return nil
		}
		return errTimeout
	}, &tried, &shares)
	c, err := d.DialContext(context.Background(), "tcp", "example.test:443")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	c.Close()
	// Families alternate, so the IPv4 address is second, not last.
	if want := []string{"[2606:4700::1]:443", "1.1.1.1:443"}; !slices.Equal(tried, want) {
		t.Fatalf("tried %v, want %v", tried, want)
	}
	if shares[0] > 4*time.Second {
		t.Fatalf("first attempt got %v of a 10 s budget over 3 addresses", shares[0])
	}
}

// TestDialStops: a refused peer or a cancelled send ends the dial; no
// further address is tried.
func TestDialStops(t *testing.T) {
	t.Run("refused peer", func(t *testing.T) {
		var tried []string
		d := stubDialer(func(string) error { return nil }, &tried, nil)
		// The name resolves to allowed addresses; by dial time the
		// check refuses them, so only the Control hook can catch it.
		connect := d.connect
		d.connect = func(ctx context.Context, nd *net.Dialer, n, addr string) (net.Conn, error) {
			d.check.SetClusterRanges([]netip.Prefix{netip.MustParsePrefix("2606:4700::/32")})
			return connect(ctx, nd, n, addr)
		}
		_, err := d.DialContext(context.Background(), "tcp", "example.test:443")
		if !errors.Is(err, ErrRefusedAddress) {
			t.Fatalf("err = %v, want ErrRefusedAddress", err)
		}
		if len(tried) != 1 {
			t.Fatalf("tried %v after a refusal, want one attempt", tried)
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		var tried []string
		ctx, cancel := context.WithCancel(context.Background())
		d := stubDialer(func(string) error { cancel(); return context.Canceled }, &tried, nil)
		if _, err := d.DialContext(ctx, "tcp", "example.test:443"); err == nil {
			t.Fatal("dial succeeded after cancel")
		}
		if len(tried) != 1 {
			t.Fatalf("tried %v after a cancel, want one attempt", tried)
		}
	})
	t.Run("budget spent", func(t *testing.T) {
		var tried []string
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(20*time.Millisecond))
		defer cancel()
		d := stubDialer(func(string) error { time.Sleep(30 * time.Millisecond); return errTimeout }, &tried, nil)
		if _, err := d.DialContext(ctx, "tcp", "example.test:443"); err == nil {
			t.Fatal("dial succeeded")
		}
		if len(tried) != 1 {
			t.Fatalf("tried %v once the budget was spent, want one attempt", tried)
		}
	})
}

func TestInterleaveFamilies(t *testing.T) {
	a := func(s string) netip.Addr { return netip.MustParseAddr(s) }
	tests := []struct {
		name string
		in   []netip.Addr
		want []netip.Addr
	}{
		{"empty", nil, nil},
		{"one", []netip.Addr{a("::1")}, []netip.Addr{a("::1")}},
		{"v6 first", []netip.Addr{a("::1"), a("::2"), a("1.1.1.1"), a("1.0.0.1")},
			[]netip.Addr{a("::1"), a("1.1.1.1"), a("::2"), a("1.0.0.1")}},
		{"v4 first, fewer v6", []netip.Addr{a("1.1.1.1"), a("1.0.0.1"), a("8.8.8.8"), a("::1")},
			[]netip.Addr{a("1.1.1.1"), a("::1"), a("1.0.0.1"), a("8.8.8.8")}},
		{"mapped counts as v4", []netip.Addr{a("::ffff:1.1.1.1"), a("2.2.2.2"), a("::1")},
			[]netip.Addr{a("::ffff:1.1.1.1"), a("::1"), a("2.2.2.2")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := interleaveFamilies(tt.in); !slices.Equal(got, tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}
