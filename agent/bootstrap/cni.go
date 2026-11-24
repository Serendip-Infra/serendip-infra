package bootstrap

import (
	_ "embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// CNI is the pod network a bootstrap installs (specs/025-networking plan
// decision 5).
type CNI string

const (
	// CNICilium installs Cilium from the first boot: K3s starts without
	// Flannel and its network policy controller, and K3s's own Helm
	// controller installs the chart from a HelmChart manifest. Recommended:
	// request metrics, flows and policy enforcement need it.
	CNICilium CNI = "cilium"
	// CNIDefault keeps K3s's bundled Flannel and network policy controller.
	CNIDefault CNI = "default"
)

// ParseCNI accepts the --cni flag's values.
func ParseCNI(s string) (CNI, error) {
	switch CNI(s) {
	case CNICilium, CNIDefault:
		return CNI(s), nil
	}
	return "", fmt.Errorf("bootstrap: unknown --cni %q (want %q or %q)", s, CNICilium, CNIDefault)
}

// The Cilium chart the agent installs. These and cilium-values.yaml are a
// copy of providers/networking/cilium's ChartRepo, ChartVersion and
// values.yaml (agent/ doesn't import providers/); TestCiliumCopyMatchesProvider
// keeps them identical.
const (
	CiliumChartRepo    = "https://helm.cilium.io"
	CiliumChartVersion = "1.20.5"
)

//go:embed cilium-values.yaml
var ciliumValues []byte

// CiliumManifestPath is where the HelmChart manifest goes: a subdirectory
// of K3s's auto-deploy directory, which K3s also reads (checked in
// spec 025 T001). K3s-uninstall.sh removes it with the rest of
// /var/lib/rancher/k3s.
const CiliumManifestPath = "/var/lib/rancher/k3s/server/manifests/serendip/cilium.yaml"

// k3sServerArgs are the K3s server flags for a CNI, passed to the install
// script as INSTALL_K3S_EXEC.
//
// --secrets-encryption is set with either CNI (specs/026 plan decision 9):
// K3s then encrypts Secrets in its datastore with a key it manages under
// /var/lib/rancher/k3s/server/cred/. The key sits on the same disk, so it
// protects a copied datastore, not a stolen disk.
func k3sServerArgs(cni CNI) []string {
	args := []string{"--secrets-encryption"}
	if cni == CNICilium {
		args = append(args, "--flannel-backend=none", "--disable-network-policy")
	}
	return args
}

// CiliumHelmChart renders the K3s HelmChart manifest that installs Cilium.
// bootstrap: true runs its install Job on the host network with the
// control-plane tolerations, before any CNI exists. The chart comes from
// helm.cilium.io at first boot, like K3s itself from get.k3s.io (an
// offline install isn't supported yet, RP-083).
func CiliumHelmChart() []byte {
	var b strings.Builder
	b.WriteString("# Written by serendip-agent bootstrap --cni cilium (specs/025-networking).\n")
	b.WriteString("apiVersion: helm.cattle.io/v1\n")
	b.WriteString("kind: HelmChart\n")
	b.WriteString("metadata:\n  name: cilium\n  namespace: kube-system\n")
	b.WriteString("spec:\n")
	b.WriteString("  repo: " + CiliumChartRepo + "\n")
	b.WriteString("  chart: cilium\n")
	b.WriteString("  version: " + strconv.Quote(CiliumChartVersion) + "\n")
	b.WriteString("  targetNamespace: kube-system\n")
	b.WriteString("  bootstrap: true\n")
	// |2-: the indentation is stated (2 past the key's), not inferred from
	// the first line, so values starting with an indented line still parse.
	b.WriteString("  valuesContent: |2-\n")
	values := strings.ReplaceAll(string(ciliumValues), "\r\n", "\n")
	for _, line := range strings.Split(strings.TrimRight(values, "\n"), "\n") {
		if line == "" {
			b.WriteString("\n")
			continue
		}
		b.WriteString("    " + line + "\n")
	}
	return []byte(b.String())
}

// Manifests returns the files Install writes before K3s starts, by path.
func Manifests(opts InstallOptions) map[string][]byte {
	if opts.CNI != CNICilium {
		return nil
	}
	return map[string][]byte{CiliumManifestPath: CiliumHelmChart()}
}

// writeManifests writes Manifests(opts) under root ("/" on a real host).
// Without Cilium it removes a Cilium manifest an earlier attempt left, or
// K3s's Helm controller would install Cilium next to Flannel.
func writeManifests(root string, opts InstallOptions) error {
	manifests := Manifests(opts)
	if _, ok := manifests[CiliumManifestPath]; !ok {
		stale := filepath.Join(root, filepath.FromSlash(CiliumManifestPath))
		if err := os.Remove(stale); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("bootstrap: remove %s: %w", stale, err)
		}
	}
	paths := make([]string, 0, len(manifests))
	for p := range manifests {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		content := manifests[p]
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			return fmt.Errorf("bootstrap: create %s: %w", filepath.Dir(full), err)
		}
		if err := writeAtomic(full, content); err != nil {
			return fmt.Errorf("bootstrap: write %s: %w", full, err)
		}
	}
	return nil
}

// writeAtomic writes content to path through a temporary file in the same
// directory, renamed into place, so K3s's manifest watcher never reads a
// half-written file.
func writeAtomic(path string, content []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp) // a no-op once renamed
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(content); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ciliumMinMajor.ciliumMinMinor is the kernel Cilium's system requirements
// name for the agent (docs.cilium.io "System Requirements", checked
// 2026-11-20).
const (
	ciliumMinMajor = 5
	ciliumMinMinor = 10
)

// kernelBelow reports whether a kernel release ("6.6.87.2-microsoft-...",
// "5.4.0-150-generic") is older than major.minor. An unreadable release
// isn't older: nothing to warn about.
func kernelBelow(release string, major, minor int) bool {
	parts := strings.SplitN(strings.TrimSpace(release), ".", 3)
	if len(parts) < 2 {
		return false
	}
	maj, err1 := strconv.Atoi(parts[0])
	minStr := parts[1]
	if i := strings.IndexFunc(minStr, func(r rune) bool { return r < '0' || r > '9' }); i >= 0 {
		minStr = minStr[:i]
	}
	mnr, err2 := strconv.Atoi(minStr)
	if err1 != nil || err2 != nil {
		return false
	}
	return maj < major || (maj == major && mnr < minor)
}

// CheckKernelForCNI warns when the running kernel is older than Cilium's
// documented minimum. A warning, not a refusal: Cilium documents a
// recommendation, not a check it makes itself, and RHEL 8.10's 4.18 is
// supported.
func CheckKernelForCNI(cni CNI) string {
	if cni != CNICilium {
		return ""
	}
	out, err := os.ReadFile("/proc/sys/kernel/osrelease")
	if err != nil {
		return ""
	}
	release := strings.TrimSpace(string(out))
	if kernelBelow(release, ciliumMinMajor, ciliumMinMinor) {
		return fmt.Sprintf("kernel %s is older than %d.%d, Cilium's documented minimum: the node may not become Ready "+
			"(use --cni default to keep K3s's Flannel)", release, ciliumMinMajor, ciliumMinMinor)
	}
	return ""
}
