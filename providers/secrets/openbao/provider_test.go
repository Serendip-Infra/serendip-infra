package openbao

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/serendip-infra/serendip-infra/core/reconcile"
	"github.com/serendip-infra/serendip-infra/core/resource"
)

func newTestSecret(path string) resource.Secret {
	return resource.Secret{
		Metadata: resource.Metadata{ID: "sec-1", Name: "test-secret"},
		Spec:     resource.SecretSpec{Path: path},
	}
}

func TestProvider_Exists_AbsentValue_ReturnsFalseNoError(t *testing.T) {
	client, _ := newTestServerClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	p := NewProvider(client)

	exists, err := p.Exists(context.Background(), newTestSecret("myapp/db-password"))
	if err != nil {
		t.Fatalf("Exists() = %v, want nil error for a 404", err)
	}
	if exists {
		t.Error("Exists() = true, want false for an absent value")
	}
}

func TestProvider_Exists_PresentValue_ReturnsTrue(t *testing.T) {
	client, _ := newTestServerClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"data": map[string]any{"password": "s3cr3t"}},
		})
	})
	p := NewProvider(client)

	exists, err := p.Exists(context.Background(), newTestSecret("myapp/db-password"))
	if err != nil {
		t.Fatalf("Exists() = %v", err)
	}
	if !exists {
		t.Error("Exists() = false, want true for a present value")
	}
}

func TestProvider_WriteValue_HitsExpectedPathWithSpecData(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	client, _ := newTestServerClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
	})
	p := NewProvider(client)

	err := p.WriteValue(context.Background(), newTestSecret("myapp/db-password"), map[string]any{"password": "s3cr3t"})
	if err != nil {
		t.Fatalf("WriteValue() = %v", err)
	}
	wantPath := "/v1/secret/data/sec-1/myapp/db-password"
	if gotPath != wantPath {
		t.Errorf("path = %q, want %q", gotPath, wantPath)
	}
	data, ok := gotBody["data"].(map[string]any)
	if !ok || data["password"] != "s3cr3t" {
		t.Errorf("body = %+v, want data.password = s3cr3t", gotBody)
	}
}

func TestProvider_ReadValue_ReturnsUnwrappedData(t *testing.T) {
	client, _ := newTestServerClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"data": map[string]any{"password": "s3cr3t"}},
		})
	})
	p := NewProvider(client)

	got, err := p.ReadValue(context.Background(), newTestSecret("myapp/db-password"))
	if err != nil {
		t.Fatalf("ReadValue() = %v", err)
	}
	if got["password"] != "s3cr3t" {
		t.Errorf("ReadValue() = %+v, want password=s3cr3t", got)
	}
}

func TestProvider_Delete_HitsMetadataPath(t *testing.T) {
	var gotPath string
	client, _ := newTestServerClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	})
	p := NewProvider(client)

	if err := p.Delete(context.Background(), newTestSecret("myapp/db-password")); err != nil {
		t.Fatalf("Delete() = %v", err)
	}
	wantPath := "/v1/secret/metadata/sec-1/myapp/db-password"
	if gotPath != wantPath {
		t.Errorf("path = %q, want %q", gotPath, wantPath)
	}
}

// Two Secrets with the same path never share a value: each is stored under
// its own ID (spec 017 T011, the alias the security review found).
func TestProvider_SamePathDifferentSecretsDontAlias(t *testing.T) {
	a := newTestSecret("payments/db-password")
	b := newTestSecret("payments/db-password")
	b.Metadata.ID = "sec-2"
	if storageKey(a) == storageKey(b) {
		t.Fatalf("both Secrets store at %q", storageKey(a))
	}
}

func TestEscapePath(t *testing.T) {
	if got := escapePath("sec-1/a b/c?d"); got != "sec-1/a%20b/c%3Fd" {
		t.Errorf("escapePath = %q", got)
	}
}

// An ID with a "/" can't forge another Secret's key: ID "sec-1/db" with
// path "pass" must not equal ID "sec-1" with path "db/pass".
func TestProvider_IDCantForgeAKey(t *testing.T) {
	victim := newTestSecret("db/pass")
	forged := newTestSecret("pass")
	forged.Metadata.ID = "sec-1/db"
	if storageKey(victim) == storageKey(forged) {
		t.Fatalf("a slash in the ID forged the key %q", storageKey(victim))
	}
}

// A sealed or missing OpenBao (503) and an unreachable one are the
// capability not being ready, not a failure (spec 026 T009 F2).
func TestProvider_Unavailable_IsProviderNotReady(t *testing.T) {
	client, _ := newTestServerClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"errors":["Vault is sealed"]}`))
	})
	p := NewProvider(client)
	sec := newTestSecret("myapp/db-password")
	_, readErr := p.ReadValue(context.Background(), sec)
	_, existsErr := p.Exists(context.Background(), sec)
	for name, err := range map[string]error{
		"read":   readErr,
		"write":  p.WriteValue(context.Background(), sec, map[string]any{"k": "v"}),
		"exists": existsErr,
		"delete": p.Delete(context.Background(), sec),
	} {
		if !errors.Is(err, reconcile.ErrProviderNotReady) {
			t.Errorf("%s: %v, want ErrProviderNotReady", name, err)
		}
	}

	down := NewClient("http://127.0.0.1:1", http.DefaultClient)
	if _, err := NewProvider(down).ReadValue(context.Background(), sec); !errors.Is(err, reconcile.ErrProviderNotReady) {
		t.Errorf("unreachable: %v, want ErrProviderNotReady", err)
	}
}

func TestProvider_OtherStatus_IsNotProviderNotReady(t *testing.T) {
	client, _ := newTestServerClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})
	if _, err := NewProvider(client).ReadValue(context.Background(), newTestSecret("p")); err == nil || errors.Is(err, reconcile.ErrProviderNotReady) {
		t.Errorf("403: %v, want a plain error", err)
	}
}

// A not-ready error carries no answer text: a 503's detail is the error's
// text (T009 review).
func TestProvider_Unavailable_HidesTheAnswer(t *testing.T) {
	client, _ := newTestServerClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"message":"no endpoints available for service \"openbao\""}`))
	})
	_, err := NewProvider(client).ReadValue(context.Background(), newTestSecret("p"))
	if !errors.Is(err, reconcile.ErrProviderNotReady) || strings.Contains(err.Error(), "endpoints") || strings.Contains(err.Error(), "openbao") {
		t.Fatalf("err = %q", err)
	}
}
