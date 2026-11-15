package main

import (
	"context"
	"errors"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/serendip-infra/serendip-infra/core/notify"
)

func TestOneSecretString(t *testing.T) {
	for _, tt := range []struct {
		name string
		data map[string]any
		want string
		ok   bool
	}{
		{"value key", map[string]any{"value": "tok", "other": "x"}, "tok", true},
		{"only key", map[string]any{"token": "tok"}, "tok", true},
		{"two keys", map[string]any{"a": "1", "b": "2"}, "", false},
		{"not a string", map[string]any{"value": 3}, "", false},
		{"empty", map[string]any{}, "", false},
	} {
		got, err := oneSecretString(tt.data)
		if got != tt.want || (err == nil) != tt.ok {
			t.Errorf("%s: %q, %v", tt.name, got, err)
		}
	}
}

// The cluster's ranges reach the sender's check (spec 024 T006): a full
// read replaces them, a partial one adds to the last, and an empty failed
// one changes nothing.
func TestRefreshClusterRanges(t *testing.T) {
	every, retry := clusterRangesEvery, clusterRangesRetry
	clusterRangesEvery, clusterRangesRetry = time.Millisecond, time.Millisecond
	t.Cleanup(func() { clusterRangesEvery, clusterRangesRetry = every, retry })

	pfx := netip.MustParsePrefix
	pods, svc, node := pfx("10.42.0.0/24"), pfx("10.43.0.0/16"), pfx("192.168.1.20/32")
	answers := []struct {
		ranges []netip.Prefix
		err    error
	}{
		{[]netip.Prefix{pods, svc}, nil},                      // full
		{[]netip.Prefix{node}, errors.New("no ServiceCIDRs")}, // partial: added
		{nil, errors.New("cluster down")},                     // nothing: kept
	}
	check := &notify.Checker{}
	ctx, cancel := context.WithCancel(context.Background())
	var mu sync.Mutex
	calls := 0
	done := make(chan struct{})
	read := func(context.Context) ([]netip.Prefix, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls > len(answers) {
			cancel()
			return nil, errors.New("stop")
		}
		a := answers[calls-1]
		return a.ranges, a.err
	}
	go func() { refreshClusterRanges(ctx, read, check); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the loop didn't stop with its context")
	}
	for _, ip := range []string{"10.42.0.9", "10.43.1.1", "192.168.1.20"} {
		if err := check.CheckAddr(netip.MustParseAddr(ip), true); !errors.Is(err, notify.ErrRefusedAddress) {
			t.Errorf("%s: %v, want refused as the cluster's own", ip, err)
		}
	}
	if err := check.CheckAddr(netip.MustParseAddr("192.168.1.30"), true); err != nil {
		t.Errorf("a LAN address with the allowance: %v", err)
	}
}
