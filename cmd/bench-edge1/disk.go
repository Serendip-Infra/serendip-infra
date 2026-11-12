package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/serendip-infra/serendip-infra/providers/kubernetes"
)

// Disk use of host-path volumes (spec 023 T002). The kubelet's Summary API
// reports no usage for hostPath-backed PersistentVolumes, which is what
// local-path (edge-1's default storage) provisions. So for those, the
// harness runs a short-lived probe pod that mounts their parent
// directories read-only and runs du: once at the start of a phase and
// once at its end, which is all growth needs. The probe is deleted
// straight after, so it doesn't sit in the measured footprint.

const (
	duProbeNamespace = "default"
	duProbeImage     = "busybox:1.36.1"
	duProbeTimeout   = 2 * time.Minute
	// duProbeLabel marks probe pods, so a probe left by a crashed run is
	// found and deleted by the next one.
	duProbeLabel = "serendip.dev/bench-disk-probe"
)

// hostPathVolume is one bound PV backed by a directory on a node.
type hostPathVolume struct {
	Claim         string // namespace/name of its PVC
	Dir           string
	Node          string // from its node affinity; empty if none
	CapacityBytes uint64
}

type pvObject struct {
	Spec struct {
		Capacity map[string]string `json:"capacity"`
		HostPath *struct {
			Path string `json:"path"`
		} `json:"hostPath"`
		Local *struct {
			Path string `json:"path"`
		} `json:"local"`
		ClaimRef *struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		} `json:"claimRef"`
		NodeAffinity *struct {
			Required struct {
				NodeSelectorTerms []struct {
					MatchExpressions []struct {
						Key    string   `json:"key"`
						Values []string `json:"values"`
					} `json:"matchExpressions"`
				} `json:"nodeSelectorTerms"`
			} `json:"required"`
		} `json:"nodeAffinity"`
	} `json:"spec"`
	Status struct {
		Phase string `json:"phase"`
	} `json:"status"`
}

// hostPathVolumes picks the bound, directory-backed PVs on nodeName (or
// with no node affinity) out of a PV list's spec and status.
func hostPathVolumes(pvs []pvObject, nodeName string) []hostPathVolume {
	var out []hostPathVolume
	for _, pv := range pvs {
		if pv.Status.Phase != "Bound" || pv.Spec.ClaimRef == nil {
			continue
		}
		dir := ""
		switch {
		case pv.Spec.HostPath != nil:
			dir = pv.Spec.HostPath.Path
		case pv.Spec.Local != nil:
			dir = pv.Spec.Local.Path
		default:
			continue
		}
		v := hostPathVolume{
			Claim:         pv.Spec.ClaimRef.Namespace + "/" + pv.Spec.ClaimRef.Name,
			Dir:           path.Clean(dir),
			CapacityBytes: parseBytesQuantity(pv.Spec.Capacity["storage"]),
		}
		if na := pv.Spec.NodeAffinity; na != nil {
			for _, t := range na.Required.NodeSelectorTerms {
				for _, e := range t.MatchExpressions {
					if e.Key == "kubernetes.io/hostname" && len(e.Values) == 1 {
						v.Node = e.Values[0]
					}
				}
			}
		}
		if v.Node != "" && v.Node != nodeName {
			continue
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Claim < out[j].Claim })
	return out
}

// parseBytesQuantity parses a Kubernetes storage quantity ("5Gi",
// "128Mi", "1G", "1000"). An unparsable one is zero.
func parseBytesQuantity(q string) uint64 {
	units := []struct {
		suffix string
		mult   float64
	}{
		{"Ki", 1 << 10}, {"Mi", 1 << 20}, {"Gi", 1 << 30}, {"Ti", 1 << 40},
		{"k", 1e3}, {"M", 1e6}, {"G", 1e9}, {"T", 1e12},
	}
	for _, u := range units {
		if strings.HasSuffix(q, u.suffix) {
			n, err := strconv.ParseFloat(strings.TrimSuffix(q, u.suffix), 64)
			if err != nil || n < 0 {
				return 0
			}
			return uint64(n * u.mult)
		}
	}
	n, err := strconv.ParseFloat(q, 64)
	if err != nil || n < 0 {
		return 0
	}
	return uint64(n)
}

// duProbePod is the probe: each distinct parent directory mounted
// read-only at /p<i>, and du -sk over every volume's directory.
func duProbePod(name, nodeName string, vols []hostPathVolume) (kubernetes.Object, map[string]string) {
	parents := map[string]int{}
	var mounts []map[string]any
	var volumes []map[string]any
	inPod := map[string]string{} // path in the pod -> claim
	var args []string
	for _, v := range vols {
		parent := path.Dir(v.Dir)
		i, ok := parents[parent]
		if !ok {
			i = len(parents)
			parents[parent] = i
			volumes = append(volumes, map[string]any{"name": fmt.Sprintf("p%d", i), "hostPath": map[string]string{"path": parent, "type": "Directory"}})
			mounts = append(mounts, map[string]any{"name": fmt.Sprintf("p%d", i), "mountPath": fmt.Sprintf("/p%d", i), "readOnly": true})
		}
		p := fmt.Sprintf("/p%d/%s", i, path.Base(v.Dir))
		inPod[p] = v.Claim
		args = append(args, p)
	}
	spec := map[string]any{
		"nodeName":                      nodeName,
		"restartPolicy":                 "Never",
		"terminationGracePeriodSeconds": 0,
		"containers": []map[string]any{{
			"name": "du", "image": duProbeImage,
			// A directory that's gone mid-run makes du fail for that one
			// line only; "true" keeps the others.
			"command":         append([]string{"sh", "-c", `du -sk "$@"; true`, "du"}, args...),
			"volumeMounts":    mounts,
			"securityContext": map[string]any{"runAsUser": 0, "readOnlyRootFilesystem": true},
			"resources":       map[string]any{"limits": map[string]string{"cpu": "100m", "memory": "32Mi"}},
		}},
		"volumes": volumes,
	}
	raw, _ := json.Marshal(spec)
	return kubernetes.Object{
		APIVersion: "v1", Kind: "Pod",
		Metadata: kubernetes.ObjectMeta{Name: name, Namespace: duProbeNamespace, Labels: map[string]string{"app.kubernetes.io/part-of": "serendip-bench", duProbeLabel: "true"}},
		Spec:     raw,
	}, inPod
}

// parseDu reads du -sk output ("<KiB>\t<path>") into used bytes per
// claim. The path is everything after the first tab, so a path with a
// space in it still matches.
func parseDu(out string, inPod map[string]string) map[string]uint64 {
	used := map[string]uint64{}
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		size, p, ok := strings.Cut(sc.Text(), "\t")
		if !ok {
			continue
		}
		claim, ok := inPod[p]
		if !ok {
			continue
		}
		kib, err := strconv.ParseUint(size, 10, 64)
		if err != nil {
			continue
		}
		used[claim] = kib << 10
	}
	return used
}

// hostPathUsage measures every host-path PV on nodeName with a probe pod.
// No such volumes gives an empty map and no pod.
func (c *cluster) hostPathUsage(ctx context.Context, nodeName string) (map[string]VolumeUsage, error) {
	objs, err := c.client.List(ctx, "/api/v1/persistentvolumes")
	if err != nil {
		return nil, fmt.Errorf("list persistent volumes: %w", err)
	}
	pvs := make([]pvObject, 0, len(objs))
	for _, o := range objs {
		var pv pvObject
		if err := json.Unmarshal(o.Spec, &pv.Spec); err != nil {
			return nil, fmt.Errorf("decode persistent volume %s: %w", o.Metadata.Name, err)
		}
		if err := json.Unmarshal(o.Status, &pv.Status); err != nil {
			return nil, fmt.Errorf("decode persistent volume %s status: %w", o.Metadata.Name, err)
		}
		pvs = append(pvs, pv)
	}
	vols := hostPathVolumes(pvs, nodeName)
	if len(vols) == 0 {
		return map[string]VolumeUsage{}, nil
	}
	podPath := "/api/v1/namespaces/" + duProbeNamespace + "/pods"
	c.deleteStaleProbes(ctx, podPath)
	name := fmt.Sprintf("bench-edge1-du-%d", time.Now().UnixNano())
	pod, inPod := duProbePod(name, nodeName, vols)
	if _, err := c.client.Create(ctx, podPath, pod); err != nil {
		// A client-side timeout can still have created it; the next
		// probe's clean-up finds it by label.
		return nil, fmt.Errorf("create disk probe: %w", err)
	}
	defer func() {
		// A fresh context: the probe must go even if ctx was cancelled.
		dctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := c.client.Delete(dctx, podPath+"/"+name); err != nil {
			log.Printf("disk probe: delete %s: %v", name, err)
		}
	}()
	deadline := time.Now().Add(duProbeTimeout)
	for {
		o, err := c.client.Get(ctx, podPath+"/"+name)
		if err != nil {
			return nil, fmt.Errorf("disk probe: %w", err)
		}
		var st struct {
			Phase string `json:"phase"`
		}
		if err := json.Unmarshal(o.Status, &st); err != nil {
			return nil, fmt.Errorf("disk probe %s: decode status: %w", name, err)
		}
		if st.Phase == "Failed" {
			return nil, fmt.Errorf("disk probe %s failed", name)
		}
		if st.Phase == "Succeeded" {
			break
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("disk probe %s not finished after %s (phase %q)", name, duProbeTimeout, st.Phase)
		}
		if err := sleep(ctx, 2*time.Second); err != nil {
			return nil, err
		}
	}
	out, err := c.getRaw(ctx, podPath+"/"+name+"/log")
	if err != nil {
		return nil, fmt.Errorf("disk probe log: %w", err)
	}
	used := parseDu(string(out), inPod)
	res := map[string]VolumeUsage{}
	var missing []string
	for _, v := range vols {
		u, ok := used[v.Claim]
		if !ok {
			missing = append(missing, v.Claim)
			continue
		}
		res[v.Claim] = VolumeUsage{UsedBytes: u, CapacityBytes: v.CapacityBytes}
	}
	if len(missing) > 0 {
		log.Printf("disk probe: no reading for %s (directory gone or unreadable)", strings.Join(missing, ", "))
	}
	return res, nil
}

// deleteStaleProbes removes probe pods a crashed or timed-out run left
// behind. It's best effort: a failure here doesn't stop the probe.
func (c *cluster) deleteStaleProbes(ctx context.Context, podPath string) {
	pods, err := c.client.List(ctx, podPath+"?labelSelector="+duProbeLabel+"%3Dtrue")
	if err != nil {
		log.Printf("disk probe: list stale probes: %v", err)
		return
	}
	for _, p := range pods {
		if err := c.client.Delete(ctx, podPath+"/"+p.Metadata.Name); err != nil {
			log.Printf("disk probe: delete stale %s: %v", p.Metadata.Name, err)
		}
	}
}

// addHostPathUsage fills in volumes the kubelet didn't report, leaving
// any it did as they are.
func addHostPathUsage(s *Sample, usage map[string]VolumeUsage) {
	for claim, u := range usage {
		if _, ok := s.Volumes[claim]; ok {
			continue
		}
		if s.Volumes == nil {
			s.Volumes = map[string]VolumeUsage{}
		}
		s.Volumes[claim] = u
	}
}
