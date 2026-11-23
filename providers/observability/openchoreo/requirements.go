package openchoreo

import "github.com/serendip-infra/serendip-infra/core/provider"

// Requirements is this package's measured cost (specs/023-observability
// T012, plan decision 8), which the package catalogue points at. As for
// builds (RP-046), the memory and CPU figures are the node's use beyond
// the same run's platform without the package, not only the package's
// own pods, so they include what it makes kube-apiserver and the
// container runtime hold. The peak is over 2.5 hours of the benchmark's
// load (T002: five loggers and steady log and metric queries).
//
// Disk is the three volumes' growth over the 2-hour disk phase under that
// load, extrapolated to a day; both stores keep logRetentionDays (3) of
// data, so the stores level off near three days of growth. The budget is
// the two stores' volumes, as usage reports them (capacityMiB); the
// Observer's 128 MiB alerts volume didn't grow. The growth is a 2-hour
// slope under a synthetic load, not a steady state (RP-063).
var Requirements = &provider.Requirements{
	IdleMemoryMiB:     608,
	IdleCPUMillicores: 21,
	PeakMemoryMiB:     967,
	PeakCPUMillicores: 372,
	MeasuredOn:        "edge-1 simulated T470 (kind, 2 CPUs of quota on 4 threads), 2026-11-14",
	Results:           "profiles/edge-1/bench/results/2026-11-14-spec023-simulated-t470/package",
	Note: "Idle, the observability package adds about 600 MiB to the node: ~275 MiB for its own pods " +
		"(OpenObserve ~125 MiB, Prometheus ~85 MiB) and the rest in kube-apiserver and the container runtime. " +
		"Under steady logging and queries it peaks near 1 GiB, with OpenObserve taking ~0.2 of a core. " +
		"Its stores grow about 500 MiB a day and keep 3 days, so expect ~1.5 GiB of disk in use.",
	Disk: &provider.DiskRequirements{
		GrowthMiBPerDay: 496,
		RetentionDays:   logRetentionDays,
		BudgetMiB:       capacityMiB,
	},
}

// TracingRequirements is the tracing package's measured cost (specs/
// 025-networking T013): the node's use with traced traffic minus the
// same traffic untraced, in the same run, on kindnet and on Cilium. The
// larger of the two runs is stated, rounded up:
//   - idle memory is the larger mean difference (Cilium, +180 MiB): the
//     collector is ~50 MiB, the rest the store holding traces, the
//     container runtime and kube-apiserver;
//   - peak memory and CPU are the traced phase's largest sample over the
//     untraced phase's mean (Cilium +366 MiB, kindnet +195 mcores);
//   - idle CPU is the larger mean difference (kindnet, +10 mcores).
//
// The traffic was one caller calling a server ten times a second, both
// sending spans (three a call). Disk is the logs store's growth over the
// hour's disk phase with traces (1226 MiB a day) less spec 023's log-only
// growth under the same log load (496 MiB a day, the 2-hour phase): a
// slope from two runs at one request rate, not a steady state (RP-088).
// The Cilium run's growth also holds Hubble's connection records and
// request series, which spec 023's run had none of, so 730 overstates
// traces alone. Traces are kept logRetentionDays in the logs store, so
// they share its 5 GiB with the logs: BudgetMiB names the same volume the
// observability package's own budget already counts.
var TracingRequirements = &provider.Requirements{
	IdleMemoryMiB:     180,
	IdleCPUMillicores: 10,
	PeakMemoryMiB:     370,
	PeakCPUMillicores: 200,
	MeasuredOn:        "edge-1 simulated T470 (kind, 2 CPUs of quota on 4 threads), 2026-11-23",
	Results:           "profiles/edge-1/bench/results/2026-11-23-spec025-simulated-t470",
	Note: "Tracing adds about 180 MiB to the node: ~50 MiB for its collector and the rest in the store keeping traces. " +
		"It uses little CPU at 10 traced requests a second (~10 mcores, peaks near 0.2 of a core). " +
		"Traces go in the observability package's logs store: at that rate about 730 MiB a day, kept 3 days, " +
		"so ~2.2 GiB of the logs store's 5 GiB; more traffic means more.",
	Disk: &provider.DiskRequirements{
		GrowthMiBPerDay: 730,
		RetentionDays:   logRetentionDays,
		BudgetMiB:       logsCapacityMiB,
	},
}
