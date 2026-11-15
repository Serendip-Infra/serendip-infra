package api

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/serendip-infra/serendip-infra/core/policy"
	"github.com/serendip-infra/serendip-infra/core/resource"
	"github.com/serendip-infra/serendip-infra/core/store"
)

// Authorization checks for the alerting kinds that need the body
// (specs/024-alerts plan decision 9), beyond the route table's rules.

var (
	channelRead      = policy.Action{Name: "serendip/notificationchannels/read"}
	allowPrivateDest = policy.Action{Name: policy.ActionAllowPrivateDestination}
)

// channelUse checks the channels a rule names: writing a rule needs read
// on each channel it newly names, so a Contributor in Project A can't
// route alerts to Project B's channel. A platform default channel (no
// owner, default) can be named by anyone who may write the rule.
type channelUse struct {
	channels store.Store[resource.NotificationChannelSpec, resource.NotificationChannelStatus]
}

func (c channelUse) check(ctx context.Context, r resource.AlertRule, current *resource.AlertRule) (context.Context, error) {
	var had []string
	// A rule moved to another owner names its channels anew there.
	if current != nil && sameOwners(r.Metadata, current.Metadata) {
		had = current.Spec.ChannelIDs
	}
	seen := map[string]bool{}
	for _, id := range r.Spec.ChannelIDs {
		// A channel the rule already named, under the same owner, was
		// checked when it was named: the rule's other fields can be edited
		// by whoever may write it.
		if seen[id] || slices.Contains(had, id) {
			continue
		}
		seen[id] = true
		ch, err := c.channels.Get(ctx, id)
		found := err == nil
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return ctx, err
		}
		if found && ch.Spec.Default && len(ch.Metadata.OwnerRefs) == 0 {
			continue
		}
		// Decided before existence, so a caller who may not read a
		// channel can't learn whether it exists.
		at := resource.Scope{Kind: resource.KindNotificationChannel, ID: id}
		var ok bool
		ctx, ok, err = allowedNoted(ctx, channelRead, at)
		if err != nil {
			return ctx, err
		}
		if !ok {
			return ctx, deployRefuse(http.StatusForbidden, "forbidden",
				"missing %s at %s: a rule sends to the channels it names, so naming one needs reading it", channelRead.Name, at)
		}
		if !found {
			return ctx, deployRefuse(http.StatusBadRequest, "validation failed", "spec.channelIds: NotificationChannel %s not found", id)
		}
	}
	return ctx, nil
}

// channelChecks are a channel's checks beyond its route's rule: the
// Secrets it names (channelSecrets) and its allowances
// (channelAllowances).
func channelChecks(ctx context.Context, r resource.NotificationChannel, current *resource.NotificationChannel) (context.Context, error) {
	ctx, err := channelSecrets(ctx, r, current)
	if err != nil {
		return ctx, err
	}
	return channelAllowances(ctx, r, current)
}

// channelSecrets: naming a Secret a channel sends with (a bot token, a
// URL that holds a token, a signing key) needs reading its value, as a
// build's credential does, since the sender reads it for the caller. It's
// checked when newly named, and for a URL Secret when its host changes.
// The SMTP server an email channel names needs nothing: SMTP servers are
// platform relays, there so Projects can send email without holding SMTP
// credentials (decision 6).
func channelSecrets(ctx context.Context, r resource.NotificationChannel, current *resource.NotificationChannel) (context.Context, error) {
	var had []string
	var hadHost string
	if current != nil {
		had = channelSecretIDs(current.Spec)
		if w := current.Spec.Webhook; w != nil {
			hadHost = w.Host
		}
	}
	for _, id := range channelSecretIDs(r.Spec) {
		w := r.Spec.Webhook
		reread := w != nil && id == w.URLSecretID && w.Host != hadHost
		if slices.Contains(had, id) && !reread {
			continue
		}
		at := resource.Scope{Kind: resource.KindSecret, ID: id}
		var ok bool
		var err error
		ctx, ok, err = allowedNoted(ctx, secretValueRead, at)
		if err != nil {
			return ctx, err
		}
		if !ok {
			return ctx, deployRefuse(http.StatusForbidden, "forbidden",
				"missing %s at %s: a channel sends with the Secrets it names, so naming one needs reading its value", secretValueRead.Name, at)
		}
	}
	return ctx, nil
}

// channelSecretIDs are the Secrets s sends with.
func channelSecretIDs(s resource.NotificationChannelSpec) []string {
	var out []string
	switch {
	case s.Telegram != nil:
		out = append(out, s.Telegram.BotTokenSecretID)
	case s.Webhook != nil:
		out = append(out, s.Webhook.URLSecretID, s.Webhook.SigningSecretID)
	}
	return slices.DeleteFunc(out, func(id string) bool { return id == "" })
}

// channelAllowances checks a channel's allowances: setting or changing
// allowPrivateDestination or allowPlainHTTP needs
// ActionAllowPrivateDestination, which only Owners hold (Contributor's
// NotActions). On update it's decided at the channel; on create, and when
// the channel moves, at each owner it's given (or "/"). Changing where a
// channel with an allowance sends needs it too: the Owner allowed one
// destination, not any.
func channelAllowances(ctx context.Context, r resource.NotificationChannel, current *resource.NotificationChannel) (context.Context, error) {
	s := r.Spec
	if s.AllowPrivateDestination == nil && s.AllowPlainHTTP == nil {
		return ctx, nil
	}
	var at []resource.Scope
	if current == nil {
		at = ownerScopes(r.Metadata)
	} else {
		// Removing an allowance only narrows what the channel reaches.
		changed := grants(s.AllowPrivateDestination, current.Spec.AllowPrivateDestination) ||
			grants(s.AllowPlainHTTP, current.Spec.AllowPlainHTTP) ||
			!sameWebhook(s.Webhook, current.Spec.Webhook)
		moved := !sameOwners(r.Metadata, current.Metadata)
		if changed {
			at = append(at, resource.Scope{Kind: resource.KindNotificationChannel, ID: r.Metadata.ID})
		}
		if changed || moved {
			at = append(at, ownerScopes(r.Metadata)...)
		}
	}
	return allowancesAt(ctx, at)
}

// sameWebhook reports whether two webhook settings send to the same place
// with the same key. (Only webhooks take allowances.)
func sameWebhook(a, b *resource.WebhookSettings) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// allowancesAt requires ActionAllowPrivateDestination at each scope. The
// heartbeat's allowance uses it too, at "/" (T007).
func allowancesAt(ctx context.Context, at []resource.Scope) (context.Context, error) {
	for _, s := range at {
		var ok bool
		var err error
		ctx, ok, err = allowedNoted(ctx, allowPrivateDest, s)
		if err != nil {
			return ctx, err
		}
		if !ok {
			return ctx, deployRefuse(http.StatusForbidden, "forbidden",
				"missing %s at %s: allowing a private destination or plain HTTP lifts the address check, so only an Owner may", allowPrivateDest.Name, s)
		}
	}
	return ctx, nil
}

// ownerScopes is m's owners as scopes, or "/" when it has none.
func ownerScopes(m resource.Metadata) []resource.Scope {
	if len(m.OwnerRefs) == 0 {
		return []resource.Scope{resource.RootScope}
	}
	out := make([]resource.Scope, 0, len(m.OwnerRefs))
	for _, o := range m.OwnerRefs {
		out = append(out, resource.Scope{Kind: o.Kind, ID: o.ID})
	}
	return out
}

// sameOwners compares two resources' owners as sets.
func sameOwners(a, b resource.Metadata) bool {
	x, y := ownerScopes(a), ownerScopes(b)
	cmp := func(p, q resource.Scope) int {
		if c := strings.Compare(string(p.Kind), string(q.Kind)); c != 0 {
			return c
		}
		return strings.Compare(p.ID, q.ID)
	}
	slices.SortFunc(x, cmp)
	slices.SortFunc(y, cmp)
	return slices.Equal(slices.Compact(x), slices.Compact(y))
}

// grants reports whether next sets an allowance that differs from was
// (a new reason is a new grant, recorded under its own decision).
func grants(next, was *resource.Allowance) bool {
	return next != nil && (was == nil || *next != *was)
}
