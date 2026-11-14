package reconcile_test

import (
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/serendip-infra/serendip-infra/core/reconcile"
)

func TestLogCursorRoundTrip(t *testing.T) {
	at := time.Date(2026, 11, 14, 10, 30, 0, 123456789, time.UTC)
	for _, tc := range []struct {
		name string
		c    reconcile.LogCursor
	}{
		{"none seen", reconcile.LogCursor{Time: at}},
		{"some seen at a tied time", reconcile.LogCursor{Time: at, Seen: 3}},
		{"more seen than one page", reconcile.LogCursor{Time: at, Seen: 1200}},
		{"whole seconds", reconcile.LogCursor{Time: at.Truncate(time.Second), Seen: 1}},
		{"a non-UTC zone", reconcile.LogCursor{Time: at.In(time.FixedZone("x", 5*3600+1800)), Seen: 2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := reconcile.DecodeLogCursor(tc.c.Encode())
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if !got.Time.Equal(tc.c.Time) || got.Seen != tc.c.Seen {
				t.Errorf("got %+v, want %+v", got, tc.c)
			}
			if got.Time.Location() != time.UTC {
				t.Errorf("decoded time in %v, want UTC", got.Time.Location())
			}
		})
	}
}

func TestDecodeLogCursorEmptyIsFirstPage(t *testing.T) {
	if got := (reconcile.LogCursor{Seen: 4}).Encode(); got != "" {
		t.Errorf("a zero-time cursor encodes as %q, want \"\"", got)
	}
	c, err := reconcile.DecodeLogCursor("")
	if err != nil || !c.Time.IsZero() || c.Seen != 0 {
		t.Errorf("DecodeLogCursor(\"\") = %+v, %v; want zero, nil", c, err)
	}
}

func TestDecodeLogCursorRefusesForeignCursors(t *testing.T) {
	enc := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	for _, tc := range []struct{ name, in string }{
		{"not base64", "%%%"},
		{"padded base64", base64.URLEncoding.EncodeToString([]byte("1:1:0"))},
		{"wrong version", enc("2:1763116200000000000:0")},
		{"too few parts", enc("1:1763116200000000000")},
		{"too many parts", enc("1:1763116200000000000:0:0")},
		{"time not a number", enc("1:yesterday:0")},
		{"zero time", enc("1:0:0")},
		{"negative time", enc("1:-5:0")},
		{"negative seen", enc("1:1763116200000000000:-1")},
		{"seen not a number", enc("1:1763116200000000000:x")},
		{"seen too large", enc("1:1763116200000000000:9999999999")},
		{"empty time", enc("1::0")},
		{"empty seen", enc("1:1763116200000000000:")},
		{"time overflows", enc("1:99999999999999999999:0")},
		{"plus sign", enc("1:+1763116200000000000:0")},
		{"leading zeros", enc("1:01763116200000000000:00")},
		{"not text", enc("\xff\xfe")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := reconcile.DecodeLogCursor(tc.in); !errors.Is(err, reconcile.ErrBadCursor) {
				t.Errorf("DecodeLogCursor(%q) = %v, want ErrBadCursor", tc.in, err)
			}
		})
	}
}
