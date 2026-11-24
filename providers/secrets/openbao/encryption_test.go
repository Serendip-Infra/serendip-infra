package openbao

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/serendip-infra/serendip-infra/core/reconcile"
	"github.com/serendip-infra/serendip-infra/providers/kubernetes"
)

func node(labels, annotations map[string]string) kubernetes.Object {
	return kubernetes.Object{Metadata: kubernetes.ObjectMeta{Labels: labels, Annotations: annotations}}
}

var (
	k3sServer = map[string]string{instanceTypeLabel: "k3s", controlPlaneLabel: "true"}
	k3sAgent  = map[string]string{instanceTypeLabel: "k3s"}
	kindNode  = map[string]string{controlPlaneLabel: ""}
	encrypted = map[string]string{k3sEncryptionAnnotation: "start-0123abcd"}
)

func TestEncryptionFromNodes(t *testing.T) {
	tests := []struct {
		name       string
		nodes      []kubernetes.Object
		wantState  reconcile.EncryptionAtRest
		wantReason string
	}{
		{"k3s server encrypted", []kubernetes.Object{node(k3sServer, encrypted)}, reconcile.EncryptionAtRestOn, reasonEncryptionConfigured},
		{"k3s server plain", []kubernetes.Object{node(k3sServer, nil)}, reconcile.EncryptionAtRestOff, reasonEncryptionNotConfigured},
		{"k3s agents are ignored", []kubernetes.Object{node(k3sServer, encrypted), node(k3sAgent, nil)}, reconcile.EncryptionAtRestOn, reasonEncryptionConfigured},
		{"k3s servers disagree", []kubernetes.Object{node(k3sServer, encrypted), node(k3sServer, nil)}, reconcile.EncryptionAtRestUnknown, reasonServersDisagree},
		{"kind", []kubernetes.Object{node(kindNode, nil)}, reconcile.EncryptionAtRestUnknown, "NotReported"},
		// The annotation alone, off a K3s node, isn't trusted.
		{"annotation without k3s label", []kubernetes.Object{node(kindNode, encrypted)}, reconcile.EncryptionAtRestUnknown, "NotReported"},
		{"only k3s agents", []kubernetes.Object{node(k3sAgent, nil)}, reconcile.EncryptionAtRestUnknown, "NotReported"},
		{"no nodes", nil, reconcile.EncryptionAtRestUnknown, "NotReported"},
		{"empty annotation", []kubernetes.Object{node(k3sServer, map[string]string{k3sEncryptionAnnotation: ""})}, reconcile.EncryptionAtRestOff, reasonEncryptionNotConfigured},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := encryptionFromNodes(tt.nodes)
			if got.State != tt.wantState || got.Reason != tt.wantReason || got.Message == "" {
				t.Errorf("encryptionFromNodes() = %+v, want %s/%s with a message", got, tt.wantState, tt.wantReason)
			}
		})
	}
}

func TestEncryptionAtRest_ListsNodes(t *testing.T) {
	var gotPath string
	kube := newTestKubeClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.Method + " " + r.URL.Path
		_ = json.NewEncoder(w).Encode(map[string]any{"items": []kubernetes.Object{node(k3sServer, encrypted)}})
	})
	got, err := NewProvider(nil).WithCluster(kube).EncryptionAtRest(context.Background())
	if err != nil || got.State != reconcile.EncryptionAtRestOn {
		t.Fatalf("EncryptionAtRest() = %+v, %v; want on", got, err)
	}
	if gotPath != "GET /api/v1/nodes" {
		t.Errorf("request = %s, want GET /api/v1/nodes", gotPath)
	}
}

func TestEncryptionAtRest_ListFails(t *testing.T) {
	kube := newTestKubeClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	})
	if got, err := NewProvider(nil).WithCluster(kube).EncryptionAtRest(context.Background()); err == nil {
		t.Errorf("EncryptionAtRest() = %+v, nil; want an error", got)
	}
}

func TestEncryptionAtRest_NoCluster(t *testing.T) {
	got, err := NewProvider(nil).EncryptionAtRest(context.Background())
	if err != nil || got.State != reconcile.EncryptionAtRestUnknown || got.Reason != "NotReported" {
		t.Errorf("EncryptionAtRest() = %+v, %v; want unknown/NotReported", got, err)
	}
}
