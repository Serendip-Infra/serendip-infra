// Package openchoreo implements the observability capability on
// OpenChoreo's observability plane (specs/023-observability, ADR 0021).
// Its Installer is the observability package: by shelling out to the real
// `helm` CLI (ADR 0010, via providers/internal/helm) it installs the plane
// chart, the OpenObserve logs module, plain Prometheus, and the metrics
// module for its adapter only; it hand-builds the placeholder and admin
// Secrets, the CA trust copy, Fluent Bit's config (off the host network,
// RP-064), and ObservabilityPlane/default through the generic
// providers/kubernetes.Client.
//
// The logs module also renders an HTTPRoute for remote Fluent Bit,
// attached to a gateway-default in the plane's namespace. With the plane
// chart's gateway off there's no such gateway, so the route attaches to
// nothing.
//
// It shares nothing with the other OpenChoreo providers, so any one can
// be deleted without breaking the others: the few lines they all need
// (the CA copy, the apply helper) are repeated here on purpose.
package openchoreo
