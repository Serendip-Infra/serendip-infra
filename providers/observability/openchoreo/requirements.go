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
