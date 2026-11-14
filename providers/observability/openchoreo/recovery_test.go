package openchoreo

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

// A locked release whose recovery fails, or that isn't pending at all,
// fails the install with both causes and runs no second upgrade.
func TestInstall_LockedReleaseRecoveryFails(t *testing.T) {
	tests := []struct {
		name    string
		status  string
		failOn  string // a verb that fails
		wantErr string
	}{
		{"not pending", "deployed", "", `the release is "deployed", not pending`},
		{"uninstall fails", "pending-install", "uninstall", "uninstall broke"},
		{"rollback fails", "pending-upgrade", "rollback", "rollback broke"},
		{"status unreadable", "", "", "read the release's status"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEnv(t)
			e.helm.respond = func(args []string) ([]byte, error) {
				switch {
				case args[0] == "upgrade" && args[2] == planeRelease:
					return []byte("Error: " + helmBusy), errors.New("exit status 1")
				case args[0] == "status" && tt.status == "":
					return []byte("garbage"), nil
				case args[0] == "status":
					return []byte(`{"info":{"status":"` + tt.status + `"}}`), nil
				case args[0] == tt.failOn:
					return nil, errors.New(tt.failOn + " broke")
				}
				return nil, nil
			}
			_, err := e.inst.Install(context.Background(), instance(""))
			if err == nil || !strings.Contains(err.Error(), helmBusy) || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Install() err = %v, want the lock and %q", err, tt.wantErr)
			}
			upgrades := 0
			for _, c := range e.helm.calls {
				if c[0] == "upgrade" {
					upgrades++
				}
			}
			if upgrades != 1 {
				t.Errorf("upgrades = %d, want 1 (no retry after a failed recovery)", upgrades)
			}
		})
	}
}

// The values file is removed whether Helm succeeds or fails.
func TestInstall_ValuesFileRemovedOnFailure(t *testing.T) {
	e := newTestEnv(t)
	var path string
	e.helm.respond = func(args []string) ([]byte, error) {
		if k := slices.Index(args, "-f"); k >= 0 {
			path = args[k+1]
			return []byte("boom"), errors.New("exit status 1")
		}
		return nil, nil
	}
	_, err := e.inst.Install(context.Background(), instance(""))
	if err == nil || !strings.Contains(err.Error(), "the metrics store") {
		t.Fatalf("Install() err = %v", err)
	}
	if path == "" {
		t.Fatal("helm never got the values file")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("values file %s left behind: %v", path, err)
	}
}

// Each repair that didn't fix a settling fault doubles the wait, up to
// maxSettle, and passing checks reset it.
func TestWaitOut_DoublesAndCaps(t *testing.T) {
	start := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	now := start
	inst := &Installer{now: func() time.Time { return now }}
	if inst.waitOut() {
		t.Fatal("with no install on record, nothing is waited out")
	}
	for _, window := range []time.Duration{5 * time.Minute, 10 * time.Minute, 20 * time.Minute, 30 * time.Minute, 30 * time.Minute} {
		inst.lastInstall = now // a repair just ran
		now = now.Add(window - time.Second)
		if !inst.waitOut() {
			t.Fatalf("window %v: not waited out just inside it", window)
		}
		now = now.Add(time.Second)
		if inst.waitOut() {
			t.Fatalf("window %v: still waited out at its end", window)
		}
	}
	inst.resetRepairs()
	inst.lastInstall = now
	now = now.Add(settle)
	if inst.waitOut() {
		t.Error("after a reset the window isn't back to settle")
	}
}
