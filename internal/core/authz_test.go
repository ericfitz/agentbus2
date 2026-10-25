package core

import (
	"context"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/store"
)

// ReadableChannelIDs takes the same precondition as access.Resolve.
func TestReadableChannelIDsRefusesReservedNames(t *testing.T) {
	c, _ := newTestCore(t, nil)
	mustIdentity(t, c, "pat", false)
	mustChannel(t, c, store.ChannelCreate{Name: "work", Kind: store.KindOrdinary, Creator: "pat"})
	for _, name := range []string{"", store.Everyone, store.Admins} {
		var ids []int64
		var err error
		mustTx(t, c, func(ctx context.Context, tx store.Tx) error {
			ids, err = c.ReadableChannelIDs(ctx, tx, name)
			return nil
		})
		if bus.CodeOf(err) != bus.CodeNotRegistered || len(ids) != 0 {
			t.Errorf("ReadableChannelIDs(%q) = %v, %v; want not_registered", name, ids, err)
		}
	}
}

func TestRequireIdentityRejectsReservedAndUnknown(t *testing.T) {
	c, _ := newTestCore(t, nil)
	mustIdentity(t, c, "pat", false)
	for _, name := range []string{store.Everyone, store.Admins, "ghost", ""} {
		var err error
		mustTx(t, c, func(ctx context.Context, tx store.Tx) error {
			_, err = c.RequireIdentity(ctx, tx, name)
			return nil
		})
		if bus.CodeOf(err) != bus.CodeNotRegistered {
			t.Errorf("RequireIdentity(%q) = %v, want not_registered", name, err)
		}
	}
	mustTx(t, c, func(ctx context.Context, tx store.Tx) error {
		id, err := c.RequireIdentity(ctx, tx, "pat")
		if err != nil || id.Name != "pat" {
			t.Errorf("RequireIdentity(pat) = %+v, %v", id, err)
		}
		return nil
	})
}

func TestRequireAdmin(t *testing.T) {
	c, _ := newTestCore(t, nil)
	mustIdentity(t, c, "pat", false)
	mustIdentity(t, c, "root", true)
	mustTx(t, c, func(ctx context.Context, tx store.Tx) error {
		if _, err := c.RequireAdmin(ctx, tx, "root"); err != nil {
			t.Errorf("root: %v", err)
		}
		_, err := c.RequireAdmin(ctx, tx, "pat")
		if bus.CodeOf(err) != bus.CodeForbidden || !strings.Contains(err.Error(), "administrator privilege required") {
			t.Errorf("pat = %v, want forbidden administrator privilege required", err)
		}
		if _, err := c.RequireAdmin(ctx, tx, "ghost"); bus.CodeOf(err) != bus.CodeNotRegistered {
			t.Errorf("ghost = %v, want not_registered", err)
		}
		return nil
	})
}

func TestChannelByNameAndReadable(t *testing.T) {
	c, _ := newTestCore(t, nil)
	mustIdentity(t, c, "pat", false)
	mustIdentity(t, c, "kim", false)
	open := mustChannel(t, c, store.ChannelCreate{Name: "open", Kind: store.KindOrdinary, Creator: "pat"})
	mustChannel(t, c, store.ChannelCreate{Name: "private", Kind: store.KindOrdinary, Creator: "pat",
		ACL: []store.ACLEntry{{Identity: "pat", Permissions: []store.Permission{store.PermRead}}}})
	mustTx(t, c, func(ctx context.Context, tx store.Tx) error {
		got, err := c.ChannelByName(ctx, tx, "open")
		if err != nil || got.ID != open.ID {
			t.Errorf("ChannelByName(open) = %+v, %v", got, err)
		}
		if _, err := c.ChannelByName(ctx, tx, "nope"); bus.CodeOf(err) != bus.CodeNotFound || !strings.Contains(err.Error(), `channel \"nope\" does not exist`) {
			t.Errorf("ChannelByName(nope) = %v", err)
		}
		ids, err := c.ReadableChannelIDs(ctx, tx, "kim")
		if err != nil || len(ids) != 1 || ids[0] != open.ID {
			t.Errorf("ReadableChannelIDs(kim) = %v, %v; want only the open channel", ids, err)
		}
		return nil
	})
}
