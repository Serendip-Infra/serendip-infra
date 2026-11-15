// Package notify sends alert notifications and the heartbeat
// (specs/024-alerts plan decisions 5, 6 and 15), standard library only.
//
// Every connection goes through one checked dialer: the address actually
// dialed is checked in the dialer's Control hook, so a name that resolves
// somewhere refused (or is re-pointed there after a check) is still
// refused. The formats spoken here (Telegram's Bot API, SMTP, Serendip's
// signed webhook) are public protocols, not products Serendip installs
// (plan decision 5's reading of constitution 1).
package notify

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"syscall"
	"time"
)

// ErrRefusedAddress is wrapped by every refusal of the address check.
var ErrRefusedAddress = errors.New("notify: destination address refused")

var (
	// alwaysRefused stays refused even with allowPrivateDestination: an
	// in-house receiver is reached by its own address, not through the
	// machine or the cluster (decision 5).
	alwaysRefused = mustPrefixes(
		"0.0.0.0/8",       // "this network", unspecified
		"127.0.0.0/8",     // loopback
		"169.254.0.0/16",  // link-local (and cloud metadata)
		"192.0.0.0/24",    // IETF protocol assignments
		"224.0.0.0/4",     // multicast
		"240.0.0.0/4",     // reserved, and broadcast
		"::/128",          // unspecified
		"::1/128",         // loopback
		"fe80::/10",       // link-local
		"ff00::/8",        // multicast
		"64:ff9b:1::/48",  // local-use NAT64
		"2001:db8::/32",   // documentation
		"2001::/32",       // Teredo: tunnels to an IPv4 address
		"::/96",           // IPv4-compatible (deprecated)
		"192.88.99.0/24",  // 6to4 relay anycast (deprecated)
		"192.0.2.0/24",    // documentation
		"198.51.100.0/24", // documentation
		"203.0.113.0/24",  // documentation
		"198.18.0.0/15",   // benchmarking
		"100::/64",        // discard-only
	)
	// private is refused unless the channel has the allowance.
	private = mustPrefixes(
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"100.64.0.0/10", // CGNAT
		"fc00::/7",      // unique local
	)
)

func mustPrefixes(ss ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(ss))
	for i, s := range ss {
		out[i] = netip.MustParsePrefix(s)
	}
	return out
}

// Checker is the address check (spec IN 9). The cluster's own ranges
// and the node's addresses are set by serendipd and refreshed with the
// cluster; until then only the fixed ranges apply.
type Checker struct {
	mu      sync.RWMutex
	cluster []netip.Prefix
	// exempt is for this package's tests only (export_test.go): the
	// test servers listen on loopback.
	exempt []netip.Prefix
}

// SetClusterRanges replaces the cluster's pod and Service ranges and the
// node's own addresses (as /32 or /128 prefixes), which stay refused
// whatever a channel allows.
func (c *Checker) SetClusterRanges(ranges []netip.Prefix) {
	cp := make([]netip.Prefix, 0, len(ranges))
	for _, p := range ranges {
		if p.IsValid() {
			cp = append(cp, p.Masked())
		}
	}
	c.mu.Lock()
	c.cluster = cp
	c.mu.Unlock()
}

// CheckAddr refuses ip if it's never reachable from here, or private
// without allowPrivate. IPv4-mapped IPv6 is checked as the IPv4 address
// it carries.
func (c *Checker) CheckAddr(ip netip.Addr, allowPrivate bool) error {
	if !ip.IsValid() {
		return fmt.Errorf("%w: not an IP address", ErrRefusedAddress)
	}
	ip = ip.Unmap().WithZone("")
	c.mu.RLock()
	cluster, exempt := c.cluster, c.exempt
	c.mu.RUnlock()
	for _, p := range exempt {
		if p.Contains(ip) {
			return nil
		}
	}
	// NAT64 and 6to4 addresses carry an IPv4 address that the network
	// may route to: check that one too.
	if v4, ok := embeddedIPv4(ip); ok {
		if err := c.CheckAddr(v4, allowPrivate); err != nil {
			return fmt.Errorf("%s carries %s: %w", ip, v4, err)
		}
	}
	for _, p := range cluster {
		if p.Contains(ip) {
			return fmt.Errorf("%w: %s is in the cluster's own ranges", ErrRefusedAddress, ip)
		}
	}
	for _, p := range alwaysRefused {
		if p.Contains(ip) {
			return fmt.Errorf("%w: %s is a loopback, link-local, multicast or reserved address", ErrRefusedAddress, ip)
		}
	}
	if !allowPrivate {
		for _, p := range private {
			if p.Contains(ip) {
				return fmt.Errorf("%w: %s is a private address; an Owner can allow a private destination for this channel", ErrRefusedAddress, ip)
			}
		}
	}
	return nil
}

var (
	nat64     = netip.MustParsePrefix("64:ff9b::/96")
	sixToFour = netip.MustParsePrefix("2002::/16")
)

// embeddedIPv4 is the IPv4 address a NAT64 (64:ff9b::/96) or 6to4
// (2002::/16) address carries.
func embeddedIPv4(ip netip.Addr) (netip.Addr, bool) {
	b := ip.As16()
	switch {
	case !ip.Is6():
		return netip.Addr{}, false
	case nat64.Contains(ip):
		return netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]}), true
	case sixToFour.Contains(ip):
		return netip.AddrFrom4([4]byte{b[2], b[3], b[4], b[5]}), true
	}
	return netip.Addr{}, false
}

// Lookup resolves a host name to its addresses.
type Lookup func(ctx context.Context, host string) ([]netip.Addr, error)

func defaultLookup(ctx context.Context, host string) ([]netip.Addr, error) {
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}

// dialer dials host:port through the check. Every address the name
// resolves to must pass (spec IN 9: refused if any of them is), and the
// Control hook checks the address actually connected to again.
type dialer struct {
	check   *Checker
	lookup  Lookup
	timeout time.Duration
}

type allowPrivateKey struct{}

// withAllowPrivate marks a send whose channel has the allowance.
func withAllowPrivate(ctx context.Context, allow bool) context.Context {
	return context.WithValue(ctx, allowPrivateKey{}, allow)
}

func allowPrivateFrom(ctx context.Context) bool {
	v, _ := ctx.Value(allowPrivateKey{}).(bool)
	return v
}

// resolve returns host's addresses, refusing the whole name if any of
// them is refused.
func (d *dialer) resolve(ctx context.Context, host string, allowPrivate bool) ([]netip.Addr, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{ip}, d.check.CheckAddr(ip, allowPrivate)
	}
	addrs, err := d.lookup(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("resolving %s: %w", host, cleanDNSError(err))
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("resolving %s: no addresses", host)
	}
	for _, a := range addrs {
		if err := d.check.CheckAddr(a, allowPrivate); err != nil {
			return nil, fmt.Errorf("%s resolves to a refused address: %w", host, err)
		}
	}
	return addrs, nil
}

// DialContext is the only way this package opens a connection.
func (d *dialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	allow := allowPrivateFrom(ctx)
	addrs, err := d.resolve(ctx, host, allow)
	if err != nil {
		return nil, err
	}
	// The address actually dialed: a second resolution can't slip past
	// the check above, because this one sees the socket's peer.
	nd := net.Dialer{Timeout: d.timeout, Control: d.control(allow)}
	var last error
	for _, a := range addrs {
		c, err := nd.DialContext(ctx, network, net.JoinHostPort(a.String(), port))
		if err == nil {
			return c, nil
		}
		last = err
		if errors.Is(err, ErrRefusedAddress) {
			return nil, err
		}
	}
	return nil, last
}

// control is the dialer's Control hook: it checks the address the socket
// is about to connect to.
func (d *dialer) control(allow bool) func(network, address string, c syscall.RawConn) error {
	return func(_, raw string, _ syscall.RawConn) error {
		ap, err := netip.ParseAddrPort(raw)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrRefusedAddress, err)
		}
		return d.check.CheckAddr(ap.Addr(), allow)
	}
}

// dnsError is a cleaned DNS failure that keeps whether it's worth a
// retry: a timeout or a failing server is, an unknown name isn't.
type dnsError struct {
	msg       string
	temporary bool
}

func (e *dnsError) Error() string   { return e.msg }
func (e *dnsError) Timeout() bool   { return e.temporary }
func (e *dnsError) Temporary() bool { return e.temporary }

// cleanDNSError keeps only what a DNS failure says about the name.
func cleanDNSError(err error) error {
	if de, ok := errors.AsType[*net.DNSError](err); ok {
		switch {
		case de.IsNotFound:
			return &dnsError{msg: "no such host"}
		case de.IsTimeout:
			return &dnsError{msg: "timed out", temporary: true}
		}
		return &dnsError{msg: "lookup failed", temporary: true}
	}
	return err
}
