package openbao

import (
	"context"
	"fmt"

	"github.com/serendip-infra/serendip-infra/core/reconcile"
	"github.com/serendip-infra/serendip-infra/providers/kubernetes"
)

// K3s's marks, checked live in spec 026 T001 (plan decision 9). A server
// started with --secrets-encryption carries the annotation (its value is
// "start-<sha>" on a fresh install); K3s labels every node it runs with
// the instance-type label.
const (
	k3sEncryptionAnnotation = "k3s.io/encryption-config-hash"
	instanceTypeLabel       = "node.kubernetes.io/instance-type"
	k3sInstanceType         = "k3s"
	controlPlaneLabel       = "node-role.kubernetes.io/control-plane"
)

// Reasons EncryptionAtRest answers with.
const (
	reasonEncryptionConfigured    = "EncryptionConfigured"
	reasonEncryptionNotConfigured = "EncryptionNotConfigured"
	reasonServersDisagree         = "ServersDisagree"
)

// WithCluster gives the Provider the cluster its delivered values rest in,
// for EncryptionAtRest. Without it the report is unknown.
func (p *Provider) WithCluster(c *kubernetes.Client) *Provider {
	p.cluster = c
	return p
}

var _ reconcile.EncryptionReporter = (*Provider)(nil)

// EncryptionAtRest reads the cluster's nodes (one LIST): K3s server nodes
// with the encryption annotation mean on, K3s server nodes without it mean
// off, and any other cluster (kind, a managed one) is unknown, because it
// doesn't say. The annotation records that encryption was configured at
// start; it doesn't prove every existing Secret was re-encrypted, which
// matters only for a cluster switched on after it held Secrets (spec 026
// leaves existing clusters alone).
func (p *Provider) EncryptionAtRest(ctx context.Context) (reconcile.EncryptionReport, error) {
	if p.cluster == nil {
		return reconcile.EncryptionNotReported(), nil
	}
	nodes, err := p.cluster.List(ctx, "/api/v1/nodes")
	if err != nil {
		return reconcile.EncryptionReport{}, fmt.Errorf("openbao: encryption at rest: list nodes: %w", err)
	}
	return encryptionFromNodes(nodes), nil
}

func encryptionFromNodes(nodes []kubernetes.Object) reconcile.EncryptionReport {
	var on, off int
	for _, n := range nodes {
		if n.Metadata.Labels[instanceTypeLabel] != k3sInstanceType {
			continue
		}
		// Only servers carry the annotation; K3s agents never do.
		if _, server := n.Metadata.Labels[controlPlaneLabel]; !server {
			continue
		}
		if n.Metadata.Annotations[k3sEncryptionAnnotation] != "" {
			on++
		} else {
			off++
		}
	}
	switch {
	case on > 0 && off == 0:
		return reconcile.EncryptionReport{
			State:   reconcile.EncryptionAtRestOn,
			Reason:  reasonEncryptionConfigured,
			Message: "K3s encrypts Secrets written to its datastore since encryption was turned on; the key is on the server's own disk, so this protects a copied datastore, not a stolen disk",
		}
	case off > 0 && on == 0:
		return reconcile.EncryptionReport{
			State:   reconcile.EncryptionAtRestOff,
			Reason:  reasonEncryptionNotConfigured,
			Message: "K3s keeps Secrets unencrypted in its datastore; operations.md, \"Secrets encryption at rest\", turns it on",
		}
	case on > 0 && off > 0:
		return reconcile.EncryptionReport{
			State:   reconcile.EncryptionAtRestUnknown,
			Reason:  reasonServersDisagree,
			Message: fmt.Sprintf("K3s servers disagree: %d with secrets encryption configured, %d without, so some Secrets may be unencrypted", on, off),
		}
	}
	return reconcile.EncryptionNotReported()
}
