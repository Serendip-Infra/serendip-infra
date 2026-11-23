package cilium

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"sort"

	"github.com/serendip-infra/serendip-infra/providers/kubernetes"
)

// Request visibility (plan decision 4): Hubble sees HTTP requests only on
// traffic Cilium's L7 proxy handles, and the proxy handles a port only
// when a CiliumNetworkPolicy has an HTTP rule on it. Allows are a union,
// so an HTTP rule admitting "anyone" would widen the isolation the
// workload's NetworkPolicy gives it. Instead each policy here copies the
// NetworkPolicy's ingress peers and ports, keeps only the HTTP ports, and
// adds an empty HTTP rule (match every request) to them. It can't admit
// anything the NetworkPolicy doesn't; a NetworkPolicy whose peers or
// ports it can't copy faithfully is skipped with a reason, never guessed.

// Target is one workload whose HTTP ports get request visibility: the
// NetworkPolicy that admits its traffic, and which of its ports carry
// HTTP. The application platform names them; cmd/serendipd passes them
// on (provider-model.md's cooperation pattern).
type Target struct {
	Namespace string
	Policy    string
	HTTPPorts []int
}

// Skip is a target left without request visibility, and why.
type Skip struct {
	Namespace, Policy, Reason string
}

// Report is what one visibility pass did.
type Report struct {
	// Applied and Deleted name the policies written, as namespace/name,
	// for the caller's audit record.
	Applied, Deleted []string
	Unchanged        int
	Skipped          []Skip
	// Failed names what a call failed for, with the error. The pass
	// carries on with the other targets.
	Failed []string
	// Disabled says why no policy is kept at all, or "".
	Disabled string
}

const (
	policyPrefix = "serendip-l7-"
	// managedLabel marks the policies this file owns, so a pass can find
	// and remove the ones no target wants any more.
	managedLabel = "serendip.dev/managed-by"
	managedValue = "networking-l7-visibility"
	cnpAPI       = "cilium.io/v2"
	// maxName is the longest object name Kubernetes takes.
	maxName = 253
)

// EnsureVisibility makes each target's L7 visibility policy match its
// NetworkPolicy and removes the policies no target wants. It writes only
// what differs, so a pass with nothing changed makes no writes.
//
//   - If the proxy would change requests (retries, path rewriting), it
//     keeps no policies: an observer mustn't change behaviour.
//   - A target whose NetworkPolicy is missing is a skip, so its policy is
//     deleted, and made again once the NetworkPolicy is back. That's on
//     purpose: without its NetworkPolicy the copy has nothing to copy.
//   - A target whose NetworkPolicy can't be read keeps the policy it has,
//     and the others go on; the failure is in Report.Failed and the error.
//   - A policy someone else added fields to is deleted and made again,
//     since an apply removes only the fields Serendip set.
//   - The node guard (nodeguard.go) is kept on every pass.
func (p *Provider) EnsureVisibility(ctx context.Context, targets []Target) (rep Report, err error) {
	defer func() { p.remember(rep, err) }()
	var errs []error
	fail := func(what string, err error) {
		rep.Failed = append(rep.Failed, what+": "+err.Error())
		errs = append(errs, err)
	}
	// The node guard is kept whatever else the pass does: the metrics
	// ports are open whether or not requests are readable.
	if wrote, err := p.ensureGuard(ctx); err != nil {
		fail(guardKey, err)
	} else if wrote {
		rep.Applied = append(rep.Applied, guardKey)
	}
	cfg, err := p.config(ctx)
	if err != nil {
		return rep, errors.Join(append(errs, err)...)
	}
	desired := map[string]cnp{} // namespace/name
	keep := map[string]bool{}   // left alone: their target couldn't be read
	if ok, notes := requestsReadable(cfg); !ok {
		rep.Disabled = notes[0]
	} else {
		for _, t := range mergeTargets(targets) {
			if err := ctx.Err(); err != nil {
				return rep, errors.Join(append(errs, err)...)
			}
			key := t.Namespace + "/" + policyPrefix + t.Policy
			obj, reason, err := p.desired(ctx, t)
			switch {
			case err != nil:
				keep[key] = true
				fail(key, err)
			case reason != "":
				rep.Skipped = append(rep.Skipped, Skip{Namespace: t.Namespace, Policy: t.Policy, Reason: reason})
			default:
				desired[key] = obj
			}
		}
	}

	existing, err := p.managed(ctx)
	if err != nil {
		return rep, errors.Join(append(errs, err)...)
	}
	for _, k := range sortedKeys(desired) {
		if err := ctx.Err(); err != nil {
			return rep, errors.Join(append(errs, err)...)
		}
		want := desired[k]
		have, ok := existing[k]
		if ok && !have.foreign && have.equal(want) {
			rep.Unchanged++
			continue
		}
		path := cnpPath(want.Metadata.Namespace, want.Metadata.Name)
		if ok && have.foreign {
			if err := p.client.Delete(ctx, path); err != nil && !errors.Is(err, kubernetes.ErrNotFound) {
				fail(k, fmt.Errorf("delete CiliumNetworkPolicy %s to remake it: %w", k, err))
				continue
			}
		}
		obj, err := want.object()
		if err == nil {
			_, err = p.client.Apply(ctx, path, obj, kubernetes.FieldManager)
		}
		if err != nil {
			fail(k, fmt.Errorf("apply CiliumNetworkPolicy %s: %w", k, err))
			continue
		}
		rep.Applied = append(rep.Applied, k)
	}
	for _, k := range sortedKeys(existing) {
		if _, ok := desired[k]; ok || keep[k] {
			continue
		}
		if err := ctx.Err(); err != nil {
			return rep, errors.Join(append(errs, err)...)
		}
		have := existing[k]
		err := p.client.Delete(ctx, cnpPath(have.Metadata.Namespace, have.Metadata.Name))
		switch {
		case errors.Is(err, kubernetes.ErrNotFound): // already gone: no write
		case err != nil:
			fail(k, fmt.Errorf("delete CiliumNetworkPolicy %s: %w", k, err))
		default:
			rep.Deleted = append(rep.Deleted, k)
		}
	}
	return rep, errors.Join(errs...)
}

// remember keeps a copy of the pass's outcome for Facts. A pass that
// failed before it got to the targets counts as failed.
func (p *Provider) remember(rep Report, err error) {
	if err != nil && len(rep.Failed) == 0 {
		rep.Failed = []string{err.Error()}
	}
	rep.Applied = slices.Clone(rep.Applied)
	rep.Deleted = slices.Clone(rep.Deleted)
	rep.Skipped = slices.Clone(rep.Skipped)
	rep.Failed = slices.Clone(rep.Failed)
	p.mu.Lock()
	p.last = &rep
	p.mu.Unlock()
}

// mergeTargets merges targets naming the same NetworkPolicy and cleans
// each one's ports: in range, sorted, once each. The caller's slices are
// left alone.
func mergeTargets(targets []Target) []Target {
	byKey := map[string]*Target{}
	for _, t := range targets {
		k := t.Namespace + "/" + t.Policy
		m, ok := byKey[k]
		if !ok {
			m = &Target{Namespace: t.Namespace, Policy: t.Policy}
			byKey[k] = m
		}
		for _, port := range t.HTTPPorts {
			if port > 0 && port <= 65535 {
				m.HTTPPorts = append(m.HTTPPorts, port)
			}
		}
	}
	out := make([]Target, 0, len(byKey))
	for _, k := range sortedKeys(byKey) {
		m := byKey[k]
		slices.Sort(m.HTTPPorts)
		m.HTTPPorts = slices.Compact(m.HTTPPorts)
		out = append(out, *m)
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func cnpPath(namespace, name string) string {
	return "/apis/cilium.io/v2/namespaces/" + url.PathEscape(namespace) + "/ciliumnetworkpolicies/" + url.PathEscape(name)
}

// existingCNP is a policy this file owns as the server has it. foreign
// says its spec holds fields this file doesn't set (someone else added
// them), which equal can't see.
type existingCNP struct {
	cnp
	foreign bool
}

// managed lists the policies this file owns, by namespace/name.
func (p *Provider) managed(ctx context.Context) (map[string]existingCNP, error) {
	raw, err := p.client.GetRaw(ctx, "/apis/cilium.io/v2/ciliumnetworkpolicies?labelSelector="+url.QueryEscape(managedLabel+"="+managedValue))
	if err != nil {
		return nil, fmt.Errorf("list Serendip's CiliumNetworkPolicies: %w", err)
	}
	var list struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("decode CiliumNetworkPolicies: %w", err)
	}
	out := make(map[string]existingCNP, len(list.Items))
	for _, item := range list.Items {
		var c existingCNP
		var spec struct {
			Spec json.RawMessage `json:"spec"`
		}
		if err := json.Unmarshal(item, &c.cnp); err != nil {
			return nil, fmt.Errorf("decode a CiliumNetworkPolicy: %w", err)
		}
		if err := json.Unmarshal(item, &spec); err != nil {
			return nil, fmt.Errorf("decode a CiliumNetworkPolicy: %w", err)
		}
		var strict cnpSpec
		c.foreign = len(spec.Spec) > 0 && strictDecode(spec.Spec, &strict) != nil
		out[c.Metadata.Namespace+"/"+c.Metadata.Name] = c
	}
	return out, nil
}

// desired reads t's NetworkPolicy and translates it, or says why not. A
// missing NetworkPolicy is a skip (the platform may not have rendered it
// yet, or the Component is going), not an error.
func (p *Provider) desired(ctx context.Context, t Target) (cnp, string, error) {
	if len(t.HTTPPorts) == 0 {
		return cnp{}, "the workload has no HTTP port", nil
	}
	if len(policyPrefix+t.Policy) > maxName {
		return cnp{}, "its network policy's name is too long to name a copy after", nil
	}
	raw, err := p.client.GetRaw(ctx, "/apis/networking.k8s.io/v1/namespaces/"+url.PathEscape(t.Namespace)+"/networkpolicies/"+url.PathEscape(t.Policy))
	if errors.Is(err, kubernetes.ErrNotFound) {
		return cnp{}, "its network policy doesn't exist yet", nil
	}
	if err != nil {
		return cnp{}, "", fmt.Errorf("read NetworkPolicy %s/%s: %w", t.Namespace, t.Policy, err)
	}
	var np networkPolicy
	if err := json.Unmarshal(raw, &np); err != nil {
		return cnp{}, "", fmt.Errorf("decode NetworkPolicy %s/%s: %w", t.Namespace, t.Policy, err)
	}
	c, reason := mirror(np, t.HTTPPorts)
	return c, reason, nil
}

// The NetworkPolicy fields mirror reads. Rules, ports and peers stay raw
// and are decoded strictly: one with a field mirror doesn't model is
// refused, not ignored.
type networkPolicy struct {
	Metadata struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
		UID       string `json:"uid"`
	} `json:"metadata"`
	Spec struct {
		PodSelector *labelSelector    `json:"podSelector"`
		PolicyTypes []string          `json:"policyTypes"`
		Ingress     []json.RawMessage `json:"ingress"`
	} `json:"spec"`
}

type labelSelector struct {
	MatchLabels      map[string]string `json:"matchLabels,omitempty"`
	MatchExpressions []selectorExpr    `json:"matchExpressions,omitempty"`
}

type selectorExpr struct {
	Key      string   `json:"key"`
	Operator string   `json:"operator"`
	Values   []string `json:"values,omitempty"`
}

// The CiliumNetworkPolicy shape this file writes and compares.
type cnp struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Name            string                      `json:"name"`
		Namespace       string                      `json:"namespace"`
		Labels          map[string]string           `json:"labels,omitempty"`
		OwnerReferences []kubernetes.OwnerReference `json:"ownerReferences,omitempty"`
	} `json:"metadata"`
	Spec cnpSpec `json:"spec"`
}

type cnpSpec struct {
	EnableDefaultDeny defaultDeny      `json:"enableDefaultDeny"`
	EndpointSelector  labelSelector    `json:"endpointSelector"`
	Ingress           []cnpIngressRule `json:"ingress"`
}

type defaultDeny struct {
	Ingress bool `json:"ingress"`
	Egress  bool `json:"egress"`
}

type cnpIngressRule struct {
	FromEndpoints []labelSelector `json:"fromEndpoints"`
	ToPorts       []cnpPortRule   `json:"toPorts"`
}

type cnpPortRule struct {
	Ports []cnpPort `json:"ports"`
	Rules httpRules `json:"rules"`
}

type cnpPort struct {
	Port     string `json:"port"`
	Protocol string `json:"protocol"`
}

// httpRules holds one empty HTTP rule: match every request, which is what
// sends the port's traffic through the proxy without filtering it.
type httpRules struct {
	HTTP []struct{} `json:"http"`
}

func (c cnp) object() (kubernetes.Object, error) {
	spec, err := json.Marshal(c.Spec)
	if err != nil {
		return kubernetes.Object{}, err
	}
	return kubernetes.Object{
		APIVersion: c.APIVersion, Kind: c.Kind,
		Metadata: kubernetes.ObjectMeta{
			Name: c.Metadata.Name, Namespace: c.Metadata.Namespace,
			Labels: c.Metadata.Labels, OwnerReferences: c.Metadata.OwnerReferences,
		},
		Spec: spec,
	}, nil
}

// equal compares what this file sets: the spec, the label and the owner.
// Both sides go through the same struct, so fields the server adds that
// this file doesn't model don't count.
func (c cnp) equal(o cnp) bool {
	a, _ := json.Marshal(c.Spec)
	b, _ := json.Marshal(o.Spec)
	return string(a) == string(b) &&
		c.Metadata.Labels[managedLabel] == o.Metadata.Labels[managedLabel] &&
		slices.Equal(c.Metadata.OwnerReferences, o.Metadata.OwnerReferences)
}

// labelKey is how a Kubernetes label is named in a Cilium selector: the
// "k8s:" source prefix keeps it to labels from Kubernetes, as a
// NetworkPolicy's selectors are.
func labelKey(k string) string { return "k8s:" + k }

const namespaceKey = "k8s:io.kubernetes.pod.namespace"

// mirror translates np into the L7 visibility policy for ports, or says
// why it can't. It only ever narrows: a rule keeps its own peers and only
// those of its ports that are in ports.
func mirror(np networkPolicy, ports []int) (cnp, string) {
	ns := np.Metadata.Namespace
	if np.Spec.PodSelector == nil {
		return cnp{}, "its network policy has no pod selector"
	}
	if !slices.Contains(np.Spec.PolicyTypes, "Ingress") {
		return cnp{}, "its network policy doesn't restrict ingress"
	}
	endpoint, reason := translateSelector(*np.Spec.PodSelector)
	if reason != "" {
		return cnp{}, "its network policy's pod selector " + reason
	}
	if len(endpoint.MatchLabels) == 0 && len(endpoint.MatchExpressions) == 0 {
		return cnp{}, "its network policy selects every pod in the namespace"
	}

	var rules []cnpIngressRule
	for _, raw := range np.Spec.Ingress {
		rule, reason := mirrorRule(raw, ns, ports)
		if reason != "" {
			return cnp{}, "its network policy has " + reason
		}
		if len(rule.ToPorts) > 0 {
			rules = append(rules, rule)
		}
	}
	if len(rules) == 0 {
		return cnp{}, "its network policy admits no traffic to its HTTP ports"
	}

	var c cnp
	c.APIVersion, c.Kind = cnpAPI, "CiliumNetworkPolicy"
	c.Metadata.Name = policyPrefix + np.Metadata.Name
	c.Metadata.Namespace = ns
	c.Metadata.Labels = map[string]string{managedLabel: managedValue}
	c.Metadata.OwnerReferences = []kubernetes.OwnerReference{{
		APIVersion: "networking.k8s.io/v1", Kind: "NetworkPolicy",
		Name: np.Metadata.Name, UID: np.Metadata.UID,
	}}
	c.Spec = cnpSpec{EndpointSelector: endpoint, Ingress: rules}
	return c, ""
}

// mirrorRule translates one ingress rule, keeping only the HTTP ports. A
// rule with none of them comes back empty; a rule mirror can't copy
// faithfully comes back with the reason.
func mirrorRule(raw json.RawMessage, ns string, httpPorts []int) (cnpIngressRule, string) {
	var rule struct {
		From  []json.RawMessage `json:"from"`
		Ports []json.RawMessage `json:"ports"`
	}
	if err := strictDecode(raw, &rule); err != nil {
		return cnpIngressRule{}, "an ingress rule Serendip doesn't know (" + err.Error() + ")"
	}

	var keep []int
	if len(rule.Ports) == 0 {
		keep = slices.Clone(httpPorts) // every port: so every HTTP port
	}
	for _, rp := range rule.Ports {
		var port struct {
			Protocol string          `json:"protocol"`
			Port     json.RawMessage `json:"port"`
			EndPort  *int            `json:"endPort"`
		}
		if err := strictDecode(rp, &port); err != nil {
			return cnpIngressRule{}, "a port Serendip doesn't know (" + err.Error() + ")"
		}
		if port.Protocol != "" && port.Protocol != "TCP" {
			continue // HTTP is TCP
		}
		if port.EndPort != nil {
			return cnpIngressRule{}, "a port range"
		}
		if len(port.Port) == 0 {
			keep = append(keep, httpPorts...) // every TCP port
			continue
		}
		var n int
		if json.Unmarshal(port.Port, &n) != nil {
			return cnpIngressRule{}, "a named port"
		}
		if slices.Contains(httpPorts, n) {
			keep = append(keep, n)
		}
	}
	slices.Sort(keep)
	keep = slices.Compact(keep)
	if len(keep) == 0 {
		return cnpIngressRule{}, ""
	}

	if len(rule.From) == 0 {
		return cnpIngressRule{}, "a rule admitting every source"
	}
	var from []labelSelector
	for _, rawPeer := range rule.From {
		sel, reason := mirrorPeer(rawPeer, ns)
		if reason != "" {
			return cnpIngressRule{}, reason
		}
		from = append(from, sel)
	}

	pr := cnpPortRule{Rules: httpRules{HTTP: []struct{}{{}}}}
	for _, n := range keep {
		pr.Ports = append(pr.Ports, cnpPort{Port: fmt.Sprint(n), Protocol: "TCP"})
	}
	return cnpIngressRule{FromEndpoints: from, ToPorts: []cnpPortRule{pr}}, ""
}

// mirrorPeer translates one peer. A pod selector alone means pods in the
// policy's own namespace; with an empty namespace selector, pods in any
// namespace (a Cilium selector without a namespace means the policy's
// own, so "any" is a namespace key that exists, checked in T001).
func mirrorPeer(raw json.RawMessage, ns string) (labelSelector, string) {
	var peer struct {
		PodSelector       *labelSelector  `json:"podSelector"`
		NamespaceSelector *labelSelector  `json:"namespaceSelector"`
		IPBlock           json.RawMessage `json:"ipBlock"`
	}
	if err := strictDecode(raw, &peer); err != nil {
		return labelSelector{}, "a peer Serendip doesn't know (" + err.Error() + ")"
	}
	if peer.IPBlock != nil {
		return labelSelector{}, "an address-range peer"
	}
	if peer.NamespaceSelector != nil && (len(peer.NamespaceSelector.MatchLabels) > 0 || len(peer.NamespaceSelector.MatchExpressions) > 0) {
		return labelSelector{}, "a peer chosen by namespace labels"
	}
	var sel labelSelector
	if peer.PodSelector != nil {
		var reason string
		if sel, reason = translateSelector(*peer.PodSelector); reason != "" {
			return labelSelector{}, "a peer whose pod selector " + reason
		}
	}
	if peer.NamespaceSelector == nil {
		if sel.MatchLabels == nil {
			sel.MatchLabels = map[string]string{}
		}
		sel.MatchLabels[namespaceKey] = ns
	} else {
		sel.MatchExpressions = append([]selectorExpr{{Key: namespaceKey, Operator: "Exists"}}, sel.MatchExpressions...)
	}
	return sel, ""
}

// translateSelector prefixes a Kubernetes selector's keys for Cilium. It
// refuses an operator it doesn't know, or a key already naming a source.
func translateSelector(s labelSelector) (labelSelector, string) {
	var out labelSelector
	if len(s.MatchLabels) > 0 {
		out.MatchLabels = make(map[string]string, len(s.MatchLabels))
	}
	for k, v := range s.MatchLabels {
		if k == "" || isSourced(k) {
			return labelSelector{}, fmt.Sprintf("uses the key %q", k)
		}
		out.MatchLabels[labelKey(k)] = v
	}
	for _, e := range s.MatchExpressions {
		switch e.Operator {
		case "In", "NotIn", "Exists", "DoesNotExist":
		default:
			return labelSelector{}, fmt.Sprintf("uses the operator %q", e.Operator)
		}
		if e.Key == "" || isSourced(e.Key) {
			return labelSelector{}, fmt.Sprintf("uses the key %q", e.Key)
		}
		out.MatchExpressions = append(out.MatchExpressions, selectorExpr{Key: labelKey(e.Key), Operator: e.Operator, Values: e.Values})
	}
	return out, ""
}

// isSourced reports whether a key already carries a Cilium source prefix
// ("k8s:", "any:", "reserved:"...), which a Kubernetes label can't.
func isSourced(k string) bool {
	for i := 0; i < len(k); i++ {
		switch k[i] {
		case ':':
			return true
		case '/':
			return false
		}
	}
	return false
}

// strictDecode decodes raw into v, refusing fields v doesn't model: a
// NetworkPolicy field mirror doesn't know may change what it admits.
func strictDecode(raw json.RawMessage, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}
