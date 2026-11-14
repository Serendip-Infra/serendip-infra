package api

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/serendip-infra/serendip-infra/core/audit"
	"github.com/serendip-infra/serendip-infra/core/reconcile"
	"github.com/serendip-infra/serendip-infra/core/resource"
	"github.com/serendip-infra/serendip-infra/core/store"
)

// PackageHandler serves GET /v1/packages: the optional packages this
// server offers (specs/020 plan decision 5), and whether each is
// available in this build. Installing one is creating a ProviderInstance
// that names it; removing it is deleting that, through an Approval.
type PackageHandler struct {
	catalogue reconcile.Catalogue
	// instances says which packages are installed, for their disk use.
	instances store.Store[resource.ProviderInstanceSpec, resource.ProviderInstanceStatus]
}

// usageTimeout bounds reading one package's disk use. The installer caps
// its own reads (one per 30 s, specs/023-observability plan decision 8).
const usageTimeout = 5 * time.Second

func (h *PackageHandler) Register(mux Mux) {
	mux.HandleFunc("GET /v1/packages", h.List)
}

// packageView is a package as GET /v1/packages lists it, with its
// measured cost (requirements, null while it's not measured) and what
// removing it loses (spec 022 plan decisions 4 and 12).
type packageView struct {
	reconcile.Package
	// Available is whether this build has the package's installer.
	Available bool `json:"available"`
	// Installable is whether it can be installed now; NotInstallable says
	// why not, such as its cost not being measured yet.
	Installable    bool   `json:"installable"`
	NotInstallable string `json:"notInstallable,omitempty"`
	// Usage is an installed package's current disk use, for one that
	// keeps data; UsageError says it couldn't be read, without the
	// backend's own words.
	Usage      *reconcile.DiskUsage `json:"usage,omitempty"`
	UsageError string               `json:"usageError,omitempty"`
}

func (h *PackageHandler) List(w http.ResponseWriter, r *http.Request) {
	installed, err := h.installed(r.Context())
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	out := []packageView{}
	for _, p := range h.catalogue {
		v := packageView{Package: p, Available: p.Installer != nil, Installable: true}
		if err := p.Installable(); err != nil {
			v.Installable, v.NotInstallable = false, err.Error()
		}
		if u, ok := p.Installer.(reconcile.PackageUsage); ok && installed[p.Provider] {
			v.Usage, v.UsageError = readUsage(r, p.Provider, u)
		}
		out = append(out, v)
	}
	slices.SortFunc(out, func(a, b packageView) int { return strings.Compare(a.Provider, b.Provider) })
	writeJSON(w, http.StatusOK, map[string]any{"packages": out})
}

// platformWide refuses an owner on a ProviderInstance: a package is
// installed for the whole platform, and an owner would let roles at that
// owner reach it (T016 security review).
func platformWide(m resource.Metadata) error {
	if len(m.OwnerRefs) > 0 {
		return fmt.Errorf("%w: metadata.ownerRefs: a package is installed platform-wide and has no owner", resource.ErrValidation)
	}
	return nil
}

// packageValidator checks a ProviderInstance names a package this server
// offers, and that its capability has no other instance: one package per
// optional capability. Two simultaneous installs could both pass the
// check; the store's unique key then refuses the second with 409
// (RP-033). The check stays for its clearer message.
func packageValidator(c reconcile.Catalogue, s store.Store[resource.ProviderInstanceSpec, resource.ProviderInstanceStatus]) func(ctx context.Context, id string, spec resource.ProviderInstanceSpec) error {
	return func(ctx context.Context, id string, spec resource.ProviderInstanceSpec) error {
		if err := c.Validate(spec); err != nil {
			return err
		}
		if id != "" {
			// Which package an instance installs is fixed: changing it
			// would leave the old one installed.
			current, err := s.Get(ctx, id)
			if err != nil {
				return err
			}
			if current.Spec.ProviderID != spec.ProviderID || current.Spec.CapabilityID != spec.CapabilityID {
				return fmt.Errorf("%w: providerId and capabilityId can't change: remove this package and install the other", resource.ErrValidation)
			}
		}
		all, err := s.List(ctx, store.ListOptions{})
		if err != nil {
			return err
		}
		for _, other := range all {
			if other.Metadata.ID != id && other.Spec.CapabilityID == spec.CapabilityID {
				return fmt.Errorf("%w: capabilityId: %s already has a package (ProviderInstance %s); remove it first", store.ErrDuplicate, spec.CapabilityID, other.Metadata.ID)
			}
		}
		return nil
	}
}

// installed is the providers that have a ProviderInstance.
func (h *PackageHandler) installed(ctx context.Context) (map[string]bool, error) {
	out := map[string]bool{}
	if h.instances == nil {
		return out, nil
	}
	all, err := h.instances.List(ctx, store.ListOptions{})
	if err != nil {
		return nil, err
	}
	for _, i := range all {
		out[i.Spec.ProviderID] = true
	}
	return out, nil
}

// readUsage reads one package's disk use. A failure is logged and said in
// general terms: the error can name the backend's internals.
func readUsage(r *http.Request, name string, u reconcile.PackageUsage) (*reconcile.DiskUsage, string) {
	ctx, cancel := context.WithTimeout(r.Context(), usageTimeout)
	defer cancel()
	du, err := u.Usage(ctx)
	if err != nil {
		log.Printf("api: %s %s: %s's disk use (request %s): %v", r.Method, r.URL.Path, name, audit.RequestIDFrom(r.Context()), err)
		return nil, "its disk use couldn't be read now; it may still be starting"
	}
	return &du, ""
}
