package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/serendip-infra/serendip-infra/providers/kubernetes"
)

// cluster reads the benchmarked cluster through its API server only: no
// in-cluster agent is installed, so the measurement doesn't add footprint.
type cluster struct {
	cfg    kubernetes.Config
	client *kubernetes.Client
}

func newCluster(kubeconfigPath string) (*cluster, error) {
	cfg, err := kubernetes.LoadConfig(kubeconfigPath)
	if err != nil {
		return nil, err
	}
	client, err := kubernetes.NewClient(cfg)
	if err != nil {
		return nil, err
	}
	return &cluster{cfg: cfg, client: client}, nil
}

// getRaw GETs path from the API server and returns the body verbatim.
// providers/kubernetes.Client decodes into a generic object envelope, which
// can't carry the kubelet Summary API's shape.
func (c *cluster) getRaw(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.Host+path, nil)
	if err != nil {
		return nil, err
	}
	if c.cfg.BearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.BearerToken)
	}
	resp, err := c.client.HTTPClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", path, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: status %d: %.200s", path, resp.StatusCode, body)
	}
	return body, nil
}

type nodeInfo struct {
	Name      string
	Kernel    string
	OSImage   string
	Runtime   string
	Kubelet   string
	CPUCap    string
	MemoryCap string
}

// node returns the single edge-1 node (the first, if there are several).
func (c *cluster) node(ctx context.Context) (nodeInfo, error) {
	nodes, err := c.client.List(ctx, "/api/v1/nodes")
	if err != nil {
		return nodeInfo{}, err
	}
	if len(nodes) == 0 {
		return nodeInfo{}, fmt.Errorf("cluster has no nodes")
	}
	var st struct {
		NodeInfo struct {
			KernelVersion           string `json:"kernelVersion"`
			OSImage                 string `json:"osImage"`
			ContainerRuntimeVersion string `json:"containerRuntimeVersion"`
			KubeletVersion          string `json:"kubeletVersion"`
		} `json:"nodeInfo"`
		Capacity map[string]string `json:"capacity"`
	}
	if err := json.Unmarshal(nodes[0].Status, &st); err != nil {
		return nodeInfo{}, fmt.Errorf("decode node status: %w", err)
	}
	return nodeInfo{
		Name: nodes[0].Metadata.Name, Kernel: st.NodeInfo.KernelVersion, OSImage: st.NodeInfo.OSImage,
		Runtime: st.NodeInfo.ContainerRuntimeVersion, Kubelet: st.NodeInfo.KubeletVersion,
		CPUCap: st.Capacity["cpu"], MemoryCap: st.Capacity["memory"],
	}, nil
}

func (c *cluster) summary(ctx context.Context, nodeName string) (Summary, error) {
	raw, err := c.getRaw(ctx, "/api/v1/nodes/"+nodeName+"/proxy/stats/summary")
	if err != nil {
		return Summary{}, err
	}
	return parseSummary(raw)
}

// notReadyPods lists pods that are neither Ready nor Succeeded.
func (c *cluster) notReadyPods(ctx context.Context) ([]string, error) {
	pods, err := c.client.List(ctx, "/api/v1/pods")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, p := range pods {
		var st struct {
			Phase      string `json:"phase"`
			Conditions []struct {
				Type   string `json:"type"`
				Status string `json:"status"`
			} `json:"conditions"`
		}
		_ = json.Unmarshal(p.Status, &st)
		if st.Phase == "Succeeded" {
			continue
		}
		ready := false
		for _, c := range st.Conditions {
			if c.Type == "Ready" && c.Status == "True" {
				ready = true
			}
		}
		if !ready {
			out = append(out, p.Metadata.Namespace+"/"+p.Metadata.Name+" ("+st.Phase+")")
		}
	}
	return out, nil
}

func (c *cluster) waitAllPodsReady(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		notReady, err := c.notReadyPods(ctx)
		if err == nil && len(notReady) == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			if err != nil {
				return err
			}
			return fmt.Errorf("pods not ready after %s: %s", timeout, strings.Join(notReady, ", "))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
}

// parseCPUQuantity parses a Kubernetes CPU quantity ("4", "3500m").
func parseCPUQuantity(q string) float64 {
	if strings.HasSuffix(q, "m") {
		n, _ := strconv.ParseFloat(strings.TrimSuffix(q, "m"), 64)
		return n / 1000
	}
	n, _ := strconv.ParseFloat(q, 64)
	return n
}

// podsWithEnv counts running workload pods (in "dp-" namespaces) whose
// first container gets the variable key, set directly or from a
// ConfigMap (OpenChoreo passes a Component's variables in one).
func (c *cluster) podsWithEnv(ctx context.Context, key string) (int, error) {
	cms, err := c.client.List(ctx, "/api/v1/configmaps")
	if err != nil {
		return 0, err
	}
	has := map[string]bool{}
	for _, cm := range cms {
		if !strings.HasPrefix(cm.Metadata.Namespace, cellNamespacePrefix) {
			continue
		}
		var data map[string]string
		_ = json.Unmarshal(cm.Data, &data)
		if _, ok := data[key]; ok {
			has[cm.Metadata.Namespace+"/"+cm.Metadata.Name] = true
		}
	}
	pods, err := c.client.List(ctx, "/api/v1/pods")
	if err != nil {
		return 0, err
	}
	n := 0
	for _, p := range pods {
		if !strings.HasPrefix(p.Metadata.Namespace, cellNamespacePrefix) {
			continue
		}
		var spec struct {
			Containers []struct {
				Env []struct {
					Name string `json:"name"`
				} `json:"env"`
				EnvFrom []struct {
					ConfigMapRef *struct {
						Name string `json:"name"`
					} `json:"configMapRef"`
				} `json:"envFrom"`
			} `json:"containers"`
		}
		var st struct {
			Phase string `json:"phase"`
		}
		_ = json.Unmarshal(p.Spec, &spec)
		_ = json.Unmarshal(p.Status, &st)
		if st.Phase != "Running" || len(spec.Containers) == 0 {
			continue
		}
		found := false
		for _, e := range spec.Containers[0].Env {
			found = found || e.Name == key
		}
		for _, f := range spec.Containers[0].EnvFrom {
			found = found || (f.ConfigMapRef != nil && has[p.Metadata.Namespace+"/"+f.ConfigMapRef.Name])
		}
		if found {
			n++
		}
	}
	return n, nil
}
