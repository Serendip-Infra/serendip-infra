package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/serendip-infra/serendip-infra/core/policy"
)

// TestObservabilityServerWideCap: 3 queries a caller and 20 in all. Seven
// callers can't all hold 3, and a freed slot goes to whoever asks next.
func TestObservabilityServerWideCap(t *testing.T) {
	h := newObservabilityHandler(nil, nil, nil, nil, nil, nil, nil)
	begin := func(caller string) (func(), int, string) {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		if caller != "" {
			r = r.WithContext(withCaller(r.Context(), policy.Principal{ID: caller}))
		}
		rec := httptest.NewRecorder()
		_, done, ok := h.begin(rec, r)
		if ok {
			return done, 0, ""
		}
		return nil, rec.Code, rec.Header().Get("Retry-After")
	}
	var dones []func()
	for c := range 6 { // 18 in all
		for range maxObsPerCaller {
			done, code, _ := begin(fmt.Sprint("caller-", c))
			if done == nil {
				t.Fatalf("caller %d refused with %d under both caps", c, code)
			}
			dones = append(dones, done)
		}
	}
	// A caller at its own cap is refused though the server has room.
	if done, code, retry := begin("caller-0"); done != nil || code != http.StatusTooManyRequests || retry == "" {
		t.Errorf("a 4th for one caller: %d, Retry-After %q", code, retry)
	}
	// The 7th caller fills the server: 2 more, then it's full for everyone.
	var last []func()
	for range maxObsPerServer - 18 {
		done, code, _ := begin("caller-6")
		if done == nil {
			t.Fatalf("a query under both caps: %d", code)
		}
		last = append(last, done)
	}
	for _, caller := range []string{"caller-6", "caller-7"} {
		if done, code, retry := begin(caller); done != nil || code != http.StatusTooManyRequests || retry == "" {
			t.Errorf("%s with the server full: %d, Retry-After %q", caller, code, retry)
		}
	}
	// A slot freed anywhere is free for anyone with room under their cap.
	dones[0]()
	done, code, _ := begin("caller-7")
	if done == nil {
		t.Fatalf("after a release: %d", code)
	}
	if again, code, _ := begin("caller-7"); again != nil || code != http.StatusTooManyRequests {
		t.Errorf("only one slot was freed: %d", code)
	}
	done()
	for _, d := range append(dones[1:], last...) {
		d()
	}
	if h.limit.all != 0 || len(h.limit.open) != 0 {
		t.Errorf("after every release: %d in all, %v open", h.limit.all, h.limit.open)
	}
	// An unauthenticated request never takes a slot.
	if done, code, _ := begin(""); done != nil || code != http.StatusUnauthorized {
		t.Errorf("no caller: %d", code)
	}
	if h.limit.all != 0 {
		t.Errorf("a refused query left %d slots taken", h.limit.all)
	}
}
