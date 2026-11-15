package openchoreo

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"sync"
	"time"

	"github.com/serendip-infra/serendip-infra/core/reconcile"
	"github.com/serendip-infra/serendip-infra/core/resource"
	"github.com/serendip-infra/serendip-infra/providers/kubernetes"
)

// The adapters scope a query by the OpenChoreo objects' UIDs, and a
// missing or unknown one silently widens it to the whole namespace (spike
// finding 5). So every scope is resolved from the live objects, and one
// with any UID missing is never sent.
//
// The object names are derived from Serendip IDs the same way the
// application and build providers derive them
// (providers/application/openchoreo/names.go,
// providers/build/openchoreo/builds.go). They're repeated here so the
// providers share nothing (doc.go).

const nameHashLen = 10

func shortHash(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:])[:nameHashLen]
}

// componentObjectName is the OpenChoreo Component for a Serendip
// Component.
func componentObjectName(compID string) string { return "cmp-" + shortHash(compID) }

// environmentObjectName is the OpenChoreo Environment for a Serendip
// Environment.
func environmentObjectName(envID string) string { return "env-" + shortHash(envID) }

// buildRunName is the WorkflowRun (and Argo Workflow) for a Build.
func buildRunName(buildID string) string {
	sum := sha256.Sum256([]byte(buildID))
	return "build-" + hex.EncodeToString(sum[:])[:16]
}

// buildsNamespace is the OpenChoreo namespace the build package creates
// every WorkflowRun in (providers/build/openchoreo BuildsNamespace).
const buildsNamespace = "serendip-builds"

func controlPlaneObjectPath(plural, name string) string {
	return "/apis/openchoreo.dev/v1alpha1/namespaces/" + controlPlaneNamespace + "/" + plural + "/" + url.PathEscape(name)
}

// componentScope is one target's adapter scope: every field is set.
type componentScope struct {
	Namespace      string `json:"namespace"`
	ProjectUID     string `json:"projectUid"`
	ComponentUID   string `json:"componentUid"`
	EnvironmentUID string `json:"environmentUid"`
}

// errNoScope says a target's platform objects don't exist (yet, or any
// more), so it has nothing stored that can be scoped exactly.
var errNoScope = errors.New("not on the platform")

// scopeTTL bounds how long a resolved UID is reused. A UID never changes
// for an object's life; the TTL only bounds how long a deleted and
// re-created object's old UID is used (RP-059). missingTTL is how long an
// object found missing stays missing, so a gone target doesn't cost GETs
// on every search.
const (
	scopeTTL   = time.Minute
	missingTTL = 5 * time.Second
)

type uidEntry struct {
	uid, project string
	missing      bool
	at           time.Time
}

// scopes resolves and caches the objects' UIDs, so a search across 20
// targets doesn't cost 60 GETs each time.
type scopes struct {
	client *kubernetes.Client
	now    func() time.Time

	mu    sync.Mutex
	cache map[string]uidEntry // by plural/name
}

func newScopes(client *kubernetes.Client, now func() time.Time) *scopes {
	return &scopes{client: client, now: now, cache: map[string]uidEntry{}}
}

// object reads plural/name's UID and, for a Component, its project's
// name.
func (s *scopes) object(ctx context.Context, plural, name string) (uidEntry, error) {
	key := plural + "/" + name
	now := s.now()
	s.mu.Lock()
	e, ok := s.cache[key]
	s.mu.Unlock()
	if ok && e.missing && now.Sub(e.at) < missingTTL {
		return uidEntry{}, fmt.Errorf("%s %s: %w", plural, name, errNoScope)
	}
	if ok && !e.missing && now.Sub(e.at) < scopeTTL {
		return e, nil
	}
	obj, err := s.client.Get(ctx, controlPlaneObjectPath(plural, name))
	if errors.Is(err, kubernetes.ErrNotFound) {
		s.mu.Lock()
		s.cache[key] = uidEntry{missing: true, at: now}
		s.mu.Unlock()
		return uidEntry{}, fmt.Errorf("%s %s: %w", plural, name, errNoScope)
	}
	if err != nil {
		return uidEntry{}, fmt.Errorf("get %s %s: %w", plural, name, err)
	}
	e = uidEntry{uid: obj.Metadata.UID, at: now}
	if plural == "components" {
		var spec struct {
			Owner struct {
				ProjectName string `json:"projectName"`
			} `json:"owner"`
		}
		if len(obj.Spec) > 0 {
			if err := json.Unmarshal(obj.Spec, &spec); err != nil {
				return uidEntry{}, fmt.Errorf("decode %s %s: %w", plural, name, err)
			}
		}
		e.project = spec.Owner.ProjectName
	}
	if e.uid == "" || (plural == "components" && e.project == "") {
		return uidEntry{}, fmt.Errorf("%s %s has no UID or project", plural, name)
	}
	s.mu.Lock()
	s.cache[key] = e
	s.mu.Unlock()
	return e, nil
}

// component resolves t's scope from its Component, the Component's
// project and its Environment. The three must belong together: the
// Deployment runs the Component and is owned by the Environment.
func (s *scopes) component(ctx context.Context, t reconcile.LogTarget) (componentScope, error) {
	compID, envID := t.Deployment.Spec.Component, t.Environment.Metadata.ID
	if compID == "" || t.Component.Metadata.ID != compID || envID == "" ||
		!slices.Contains(t.Deployment.Metadata.OwnerRefs, resource.OwnerRef{Kind: resource.KindEnvironment, ID: envID}) {
		return componentScope{}, fmt.Errorf("target %s: the Deployment, Component and Environment don't match", t.Deployment.Metadata.ID)
	}
	comp, err := s.object(ctx, "components", componentObjectName(compID))
	if err != nil {
		return componentScope{}, err
	}
	proj, err := s.object(ctx, "projects", comp.project)
	if err != nil {
		return componentScope{}, err
	}
	env, err := s.object(ctx, "environments", environmentObjectName(t.Environment.Metadata.ID))
	if err != nil {
		return componentScope{}, err
	}
	return componentScope{Namespace: controlPlaneNamespace, ProjectUID: proj.uid, ComponentUID: comp.uid, EnvironmentUID: env.uid}, nil
}
