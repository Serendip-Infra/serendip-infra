package sqlite_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/serendip-infra/serendip-infra/core/resource"
	"github.com/serendip-infra/serendip-infra/core/store"
	"github.com/serendip-infra/serendip-infra/core/store/sqlite"
	"github.com/serendip-infra/serendip-infra/core/store/storetest"
)

// Spec 024 plan decision 1: a Secret, SMTP server or channel that
// something still names can't be deleted (409), while one that a
// cascade removes together with its user can.
func TestDelete_RefusedWhileInUse(t *testing.T) {
	ctx := context.Background()
	db := newCascadeTestDB(t)

	projects := sqlite.NewStore[resource.ProjectSpec, resource.ProjectStatus](db, resource.KindProject)
	comps := sqlite.NewStore[resource.ComponentSpec, resource.ComponentStatus](db, resource.KindComponent)
	secrets := sqlite.NewStore[resource.SecretSpec, resource.SecretStatus](db, resource.KindSecret)
	smtps := sqlite.NewStore[resource.SMTPServerSpec, resource.SMTPServerStatus](db, resource.KindSMTPServer)
	channels := sqlite.NewStore[resource.NotificationChannelSpec, resource.NotificationChannelStatus](db, resource.KindNotificationChannel)
	rules := sqlite.NewStore[resource.AlertRuleSpec, resource.AlertRuleStatus](db, resource.KindAlertRule)

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	user, err := secrets.Create(ctx, resource.Secret{Metadata: resource.Metadata{Name: "smtp-user"}, Spec: resource.SecretSpec{Path: "smtp/user"}})
	must(err)
	pass, err := secrets.Create(ctx, resource.Secret{Metadata: resource.Metadata{Name: "smtp-pass"}, Spec: resource.SecretSpec{Path: "smtp/pass"}})
	must(err)
	smtpSpec := resource.SMTPServerSpec{Host: "smtp.example.com", Port: 587, TLS: resource.SMTPStartTLS, From: "a@example.com",
		UsernameSecretID: user.Metadata.ID, PasswordSecretID: pass.Metadata.ID}
	smtp, err := smtps.Create(ctx, resource.SMTPServer{Metadata: resource.Metadata{Name: "gmail", Relationships: resource.SMTPServerRelationships(smtpSpec)}, Spec: smtpSpec})
	must(err)

	proj, err := projects.Create(ctx, resource.Project{Metadata: resource.Metadata{Name: "web"}})
	must(err)
	inProject := []resource.OwnerRef{{Kind: resource.KindProject, ID: proj.Metadata.ID}}
	chSpec := resource.NotificationChannelSpec{Type: resource.ChannelEmail, Email: &resource.EmailSettings{SMTPServerID: smtp.Metadata.ID, To: []string{"b@example.com"}}}
	ch, err := channels.Create(ctx, resource.NotificationChannel{Metadata: resource.Metadata{Name: "ops-mail", OwnerRefs: inProject,
		Relationships: resource.NotificationChannelRelationships(chSpec)}, Spec: chSpec})
	must(err)
	comp, err := comps.Create(ctx, resource.Component{Metadata: resource.Metadata{Name: "api", OwnerRefs: inProject}})
	must(err)
	ruleSpec := resource.AlertRuleSpec{ChannelIDs: []string{ch.Metadata.ID}}
	_, err = rules.Create(ctx, resource.AlertRule{Metadata: resource.Metadata{Name: "cpu",
		OwnerRefs:     []resource.OwnerRef{{Kind: resource.KindComponent, ID: comp.Metadata.ID}},
		Relationships: resource.AlertRuleRelationships(ruleSpec)}, Spec: ruleSpec})
	must(err)

	refused := []struct {
		name string
		del  func() error
		user string
	}{
		{"a Secret an SMTP server uses", func() error {
			return secrets.Delete(storetest.Approved(ctx, resource.KindSecret, pass.Metadata.ID, false), pass.Metadata.ID, false)
		}, "SMTPServer gmail"},
		{"an SMTP server a channel uses", func() error {
			return smtps.Delete(storetest.Approved(ctx, resource.KindSMTPServer, smtp.Metadata.ID, false), smtp.Metadata.ID, false)
		}, "NotificationChannel ops-mail"},
		{"a channel a rule uses", func() error {
			return channels.Delete(storetest.Approved(ctx, resource.KindNotificationChannel, ch.Metadata.ID, false), ch.Metadata.ID, false)
		}, "AlertRule cpu"},
	}
	for _, tt := range refused {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.del()
			if !errors.Is(err, store.ErrInUse) || !strings.Contains(err.Error(), tt.user) {
				t.Fatalf("Delete = %v, want ErrInUse naming %s", err, tt.user)
			}
		})
	}

	// The Project's cascade removes the channel with the rule (through
	// the Component) that uses it: nothing outside still depends on it.
	must(projects.Delete(storetest.Approved(ctx, resource.KindProject, proj.Metadata.ID, true), proj.Metadata.ID, true))
	if _, err := channels.Get(ctx, ch.Metadata.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("channel after the cascade: %v", err)
	}
	// Now nothing names the SMTP server, so it goes; then its Secrets.
	must(smtps.Delete(storetest.Approved(ctx, resource.KindSMTPServer, smtp.Metadata.ID, false), smtp.Metadata.ID, false))
	must(secrets.Delete(storetest.Approved(ctx, resource.KindSecret, pass.Metadata.ID, false), pass.Metadata.ID, false))
}

// A cascade that would remove a used channel while its user stays is
// refused too: here the rule names a channel in another Project.
func TestDelete_CascadeRefusedWhileUsedOutside(t *testing.T) {
	ctx := context.Background()
	db := newCascadeTestDB(t)
	projects := sqlite.NewStore[resource.ProjectSpec, resource.ProjectStatus](db, resource.KindProject)
	channels := sqlite.NewStore[resource.NotificationChannelSpec, resource.NotificationChannelStatus](db, resource.KindNotificationChannel)
	rules := sqlite.NewStore[resource.AlertRuleSpec, resource.AlertRuleStatus](db, resource.KindAlertRule)
	comps := sqlite.NewStore[resource.ComponentSpec, resource.ComponentStatus](db, resource.KindComponent)

	a, _ := projects.Create(ctx, resource.Project{Metadata: resource.Metadata{Name: "a"}})
	b, _ := projects.Create(ctx, resource.Project{Metadata: resource.Metadata{Name: "b"}})
	ch, err := channels.Create(ctx, resource.NotificationChannel{Metadata: resource.Metadata{Name: "b-hook",
		OwnerRefs: []resource.OwnerRef{{Kind: resource.KindProject, ID: b.Metadata.ID}}}})
	if err != nil {
		t.Fatal(err)
	}
	comp, _ := comps.Create(ctx, resource.Component{Metadata: resource.Metadata{Name: "api",
		OwnerRefs: []resource.OwnerRef{{Kind: resource.KindProject, ID: a.Metadata.ID}}}})
	spec := resource.AlertRuleSpec{ChannelIDs: []string{ch.Metadata.ID}}
	if _, err := rules.Create(ctx, resource.AlertRule{Metadata: resource.Metadata{Name: "r",
		OwnerRefs:     []resource.OwnerRef{{Kind: resource.KindComponent, ID: comp.Metadata.ID}},
		Relationships: resource.AlertRuleRelationships(spec)}, Spec: spec}); err != nil {
		t.Fatal(err)
	}
	if err := projects.Delete(storetest.Approved(ctx, resource.KindProject, b.Metadata.ID, true), b.Metadata.ID, true); !errors.Is(err, store.ErrInUse) {
		t.Fatalf("Delete(project b, cascade) = %v, want ErrInUse", err)
	}
	if _, err := channels.Get(ctx, ch.Metadata.ID); err != nil {
		t.Fatalf("channel removed despite the refusal: %v", err)
	}
}
