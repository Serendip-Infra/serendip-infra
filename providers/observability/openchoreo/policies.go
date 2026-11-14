package openchoreo

// The plane's NetworkPolicies (plan decision 4, spike findings 8 and 9).
// The plane chart's own policy admits only pods in the namespace, which
// keeps workloads away from the Observer, the adapters, the stores and
// Fluent Bit's :2020. Fluent Bit runs off the host network (RP-064,
// fluentbit.go), so its flushes come from a pod in the namespace and need
// no rule of Serendip's. The logs store's own credentials still apply.
const (
	// chartPolicyName is the plane chart's ingress policy, whose presence
	// the checks require: without it the plane is open to every pod.
	chartPolicyName = "observability-plane-ingress"
	logsStorePort   = 5080
)

// logsStoreSelector selects the logs store's pod in the readiness check.
var logsStoreSelector = map[string]string{
	"app.kubernetes.io/name":     "openobserve-standalone",
	"app.kubernetes.io/instance": logsRelease,
}

const networkPoliciesPath = "/apis/networking.k8s.io/v1/namespaces/" + PlaneNamespace + "/networkpolicies"
