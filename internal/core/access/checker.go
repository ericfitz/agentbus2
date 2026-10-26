package access

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/store"
)

// Config is the one configuration the rule reads. ChannelCreation is the
// core.Config.ChannelCreation value ("open" or "admins", Decision 93).
type Config struct{ ChannelCreation string }

// Checker loads the facts of an object inside a transaction and applies
// decide. It holds nothing else.
type Checker struct{ Cfg Config }

// Option adjusts how Require words a denial.
type Option func(*requireOptions)

type requireOptions struct{ text, notFound string }

// WithText replaces the text of a forbidden denial (a site that words the
// administrator refusal for its own operation). Other denials keep their text.
func WithText(text string) Option { return func(o *requireOptions) { o.text = text } }

// WithNotFound words the not_found denial of an object outside the principal's
// scope (Reason out_of_scope) as the operation words a thing that does not
// exist, for the by-id operations whose unknown-id text is not the channel's.
func WithNotFound(format string, args ...any) Option {
	return func(o *requireOptions) { o.notFound = fmt.Sprintf(format, args...) }
}

// Check loads the object's facts in tx and decides. It never panics: an
// undefined (object, action) pair is an internal decision.
func (c Checker) Check(ctx context.Context, tx store.Tx, p Principal, o Object, a Action) (Decision, error) {
	f, early, err := c.load(ctx, tx, o)
	if err != nil {
		return Decision{}, err
	}
	if early != nil {
		return *early, nil
	}
	return decide(p, f, a), nil
}

// load returns the facts of o, or a ready denial when the object does not
// exist (a missing session or identity; a missing channel is an error).
func (c Checker) load(ctx context.Context, tx store.Tx, o Object) (facts, *Decision, error) {
	switch o := o.(type) {
	case Channel:
		ch, err := tx.Channel(ctx, o.ID)
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil, bus.Errorf(bus.CodeNotFound, false, "channel %d does not exist", o.ID)
		}
		if err != nil {
			return nil, nil, fromStore(err)
		}
		rows, err := tx.ACL(ctx, o.ID)
		if err != nil {
			return nil, nil, fromStore(err)
		}
		return channelFacts{Channel: ch, Rows: rows}, nil, nil
	case Server:
		return serverFacts{ChannelCreation: c.Cfg.ChannelCreation}, nil, nil
	case Session:
		sess, err := tx.Session(ctx, o.ID)
		if errors.Is(err, store.ErrNotFound) {
			d := deny(bus.CodeNotFound, ReasonNotOwner, "session %q does not exist", o.ID)
			return nil, &d, nil
		}
		if err != nil {
			return nil, nil, fromStore(err)
		}
		return sessionFacts{sess}, nil, nil
	case Identity:
		id, err := tx.Identity(ctx, o.Name)
		if errors.Is(err, store.ErrNotFound) {
			d := deny(bus.CodeNotFound, ReasonNotRegistered, "identity %q is not registered", o.Name)
			return nil, &d, nil
		}
		if err != nil {
			return nil, nil, fromStore(err)
		}
		return identityFacts{id}, nil, nil
	}
	return nil, nil, bus.Internal(errors.New("access: unknown object kind"))
}

// Require is Check, returning a denial as the *bus.Error the object kind
// uses. A missing session or identity is its kind's not_found.
func (c Checker) Require(ctx context.Context, tx store.Tx, p Principal, o Object, a Action, opts ...Option) error {
	d, err := c.Check(ctx, tx, p, o, a)
	if err != nil {
		return err
	}
	if d.Allowed {
		return nil
	}
	var ro requireOptions
	for _, opt := range opts {
		opt(&ro)
	}
	text := d.Text
	if ro.text != "" && d.Code == bus.CodeForbidden {
		text = ro.text
	}
	if ro.notFound != "" && d.Reason == ReasonOutOfScope {
		text = ro.notFound
	}
	return bus.Errorf(d.Code, false, "%s", text) // never retryable, internal included (spec 3.6)
}

// channelActions are the actions Channels accepts.
func channelAction(a Action) bool {
	switch a {
	case Read, Write, Admin, List, Deliver, Post:
		return true
	}
	return false
}

// Channels is the set form: the ids of every channel for which Check would
// allow a, ascending.
func (c Checker) Channels(ctx context.Context, tx store.Tx, p Principal, a Action) ([]int64, error) {
	if !channelAction(a) {
		d := undefined(channelFacts{}, a)
		return nil, bus.Errorf(d.Code, false, "%s", d.Text)
	}
	if p == Anonymous {
		return nil, nil
	}
	chans, err := tx.ListChannels(ctx)
	if err != nil {
		return nil, fromStore(err)
	}
	names := []string{p.Name, store.Everyone}
	if actsAsAdmin(p) {
		names = append(names, store.Admins)
	}
	if p.Kind == store.IdentityHuman {
		names = append(names, store.Humans)
	}
	rows, err := tx.ACLRowsFor(ctx, names...)
	if err != nil {
		return nil, fromStore(err)
	}
	byChannel := make(map[int64][]store.ACLEntry)
	for _, r := range rows {
		var perms []store.Permission
		if r.Read {
			perms = append(perms, store.PermRead)
		}
		if r.Write {
			perms = append(perms, store.PermWrite)
		}
		if r.Admin {
			perms = append(perms, store.PermAdmin)
		}
		byChannel[r.ChannelID] = append(byChannel[r.ChannelID], store.ACLEntry{Identity: r.Identity, Permissions: perms})
	}
	var ids []int64
	for _, ch := range chans {
		if decide(p, channelFacts{Channel: ch, Rows: byChannel[ch.ID]}, a).Allowed {
			ids = append(ids, ch.ID)
		}
	}
	slices.Sort(ids)
	return ids, nil
}

// Identities is the set form over identities: those p may See, by name.
func (c Checker) Identities(ctx context.Context, tx store.Tx, p Principal) ([]store.Identity, error) {
	all, err := tx.ListIdentities(ctx)
	if err != nil {
		return nil, fromStore(err)
	}
	var out []store.Identity
	for _, id := range all {
		if decide(p, identityFacts{id}, See).Allowed {
			out = append(out, id)
		}
	}
	return out, nil
}

// Holders is the reverse set form: the registered principals for whom Check
// allows a on the channel, by name. The reserved names are not principals and
// never appear.
func (c Checker) Holders(ctx context.Context, tx store.Tx, o Channel, a Action) ([]Principal, error) {
	if !channelAction(a) {
		d := undefined(channelFacts{}, a)
		return nil, bus.Errorf(d.Code, false, "%s", d.Text)
	}
	f, _, err := c.load(ctx, tx, o)
	if err != nil {
		return nil, err
	}
	all, err := tx.ListIdentities(ctx)
	if err != nil {
		return nil, fromStore(err)
	}
	var out []Principal
	for _, id := range all {
		if store.ReservedIdentity(id.Name) {
			continue
		}
		p := principalOf(id)
		if decide(p, f, a).Allowed {
			out = append(out, p)
		}
	}
	return out, nil
}
