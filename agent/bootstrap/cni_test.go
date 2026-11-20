package bootstrap

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The agent can't import providers/networking/cilium (agent/ imports
// nothing from providers/ or core/ beyond core/id), so it carries a copy of
// the chart pin and values. This keeps the copy identical, as spec 023 did
// for the bench's Prometheus values.
func TestCiliumCopyMatchesProvider(t *testing.T) {
	norm := func(b []byte) []byte { return bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n")) }
	dir := filepath.Join("..", "..", "providers", "networking", "cilium")

	want, err := os.ReadFile(filepath.Join(dir, "values.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(norm(want), norm(ciliumValues)) {
		t.Error("agent/bootstrap/cilium-values.yaml differs from providers/networking/cilium/values.yaml; change both together")
	}

	src, err := os.ReadFile(filepath.Join(dir, "cilium.go"))
	if err != nil {
		t.Fatal(err)
	}
	for name, got := range map[string]string{"ChartVersion": CiliumChartVersion, "ChartRepo": CiliumChartRepo} {
		m := regexp.MustCompile(`(?m)^\s*` + name + `\s*=\s*"([^"]+)"`).FindSubmatch(src)
		if m == nil {
			t.Fatalf("no %s in the provider's cilium.go", name)
		}
		if string(m[1]) != got {
			t.Errorf("provider %s = %q, agent's copy = %q; change both together", name, m[1], got)
		}
	}
}

func TestParseCNI(t *testing.T) {
	for _, s := range []string{"cilium", "default"} {
		if got, err := ParseCNI(s); err != nil || string(got) != s {
			t.Errorf("ParseCNI(%q) = %q, %v", s, got, err)
		}
	}
	for _, s := range []string{"", "flannel", "Cilium"} {
		if _, err := ParseCNI(s); err == nil {
			t.Errorf("ParseCNI(%q) accepted", s)
		}
	}
}

func TestInstallEnv_CNI(t *testing.T) {
	cilium := InstallEnv(InstallOptions{CNI: CNICilium})
	if !slices.Contains(cilium, "INSTALL_K3S_EXEC=server --flannel-backend=none --disable-network-policy") {
		t.Errorf("InstallEnv(cilium) = %v", cilium)
	}
	for _, cni := range []CNI{CNIDefault, ""} {
		for _, e := range InstallEnv(InstallOptions{CNI: cni}) {
			if strings.HasPrefix(e, "INSTALL_K3S_EXEC=") {
				t.Errorf("InstallEnv(%q) sets %s", cni, e)
			}
		}
	}
}

func TestCiliumHelmChart(t *testing.T) {
	got := string(CiliumHelmChart())
	for _, want := range []string{
		"apiVersion: helm.cattle.io/v1\nkind: HelmChart\n",
		"  name: cilium\n  namespace: kube-system\n",
		"  repo: https://helm.cilium.io\n",
		"  version: \"" + CiliumChartVersion + "\"\n",
		"  bootstrap: true\n",
		"  valuesContent: |2-\n    # Cilium Helm values",
		"\n    ipam:\n      mode: kubernetes\n",
		"\n    extraConfig:\n      http-retry-count: \"0\"\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("manifest lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "\r") {
		t.Error("manifest has CR characters")
	}
	// Every values line is inside the block scalar.
	_, block, _ := strings.Cut(got, "valuesContent: |2-\n")
	for _, line := range strings.Split(strings.TrimRight(block, "\n"), "\n") {
		if line != "" && !strings.HasPrefix(line, "    ") {
			t.Errorf("values line not indented: %q", line)
		}
	}
}

func TestWriteManifests(t *testing.T) {
	root := t.TempDir()
	if err := writeManifests(root, InstallOptions{CNI: CNICilium}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(CiliumManifestPath)))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, CiliumHelmChart()) {
		t.Error("written manifest differs from CiliumHelmChart()")
	}

	// A later --cni default removes what an earlier --cni cilium wrote.
	if err := writeManifests(root, InstallOptions{CNI: CNIDefault}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(CiliumManifestPath))); !os.IsNotExist(err) {
		t.Errorf("stale Cilium manifest kept: %v", err)
	}

	empty := t.TempDir()
	if err := writeManifests(empty, InstallOptions{CNI: CNIDefault}); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(empty); len(entries) != 0 {
		t.Errorf("--cni default wrote %v", entries)
	}

	// A file where the directory should be fails clearly.
	blocked := t.TempDir()
	if err := os.WriteFile(filepath.Join(blocked, "var"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeManifests(blocked, InstallOptions{CNI: CNICilium}); err == nil {
		t.Error("writeManifests succeeded with a file blocking its directory")
	}
}

func TestKernelBelow(t *testing.T) {
	for _, tc := range []struct {
		release string
		below   bool
	}{
		{"6.6.87.2-microsoft-standard-WSL2", false},
		{"5.10.0-28-amd64", false},
		{"5.15.0-122-generic", false},
		{"5.4.0-150-generic", true},
		{"4.19.0", true},
		{"5.9", true},
		{"", false},
		{"garbage", false},
	} {
		if got := kernelBelow(tc.release, 5, 10); got != tc.below {
			t.Errorf("kernelBelow(%q) = %v, want %v", tc.release, got, tc.below)
		}
	}
}
