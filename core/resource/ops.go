package resource

// KindIncident is the operational kind operations.md correlates Alerts
// into (specs 028 and 029). KindAlert lives in alerting.go (spec 024).
const KindIncident Kind = "Incident"

// IncidentSpec captures what's declared about an Incident up front.
// Diagnosis and the approval/action trail (docs/architecture/operations.md
// full lifecycle) are explicitly out of scope for spec 001 -- types only.
type IncidentSpec struct {
	Severity string `json:"severity,omitempty"`
	Summary  string `json:"summary,omitempty"`
}

// IncidentStatus tracks what an Incident has correlated so far. Nothing
// populates CorrelatedAlerts or AffectedResources in spec 001 -- that's
// operations.md's correlation engine and ai-operations.md's blast-radius
// output, both later specs. The shape exists now so those specs don't
// need a breaking change to add it.
type IncidentStatus struct {
	Status
	CorrelatedAlerts  []string `json:"correlatedAlerts,omitempty"`  // Alert IDs
	AffectedResources []string `json:"affectedResources,omitempty"` // resource IDs
}

type Incident = Resource[IncidentSpec, IncidentStatus]
