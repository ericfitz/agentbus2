package access

import (
	"context"
	"errors"
	"math/rand/v2"
	"reflect"
	"slices"
	"testing"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core/access"
	"github.com/ericfitz/agentbus2/internal/dbtest"
	"github.com/ericfitz/agentbus2/internal/store"
)

const (
	halName  = "hal@corp.example"
	hal2Name = "hal2@corp.example"
)

var checker = access.Checker{Cfg: access.Config{ChannelCreation: "open"}}

// seed registers root (admin), pat, kim, two humans and a scope job1 with the
// scoped identity job1-coder.
func seed(t *testing.T, s store.Store) {
	t.Helper()
	mustTx(t, s, func(ctx context.Context, tx store.Tx) error {
		for _, n := range []string{"root", "pat", "kim"} {
			if err := tx.CreateIdentity(ctx, n, now); err != nil {
				return err
			}
		}
		if err := tx.SetAdmin(ctx, "root", true); err != nil {
			return err
		}
		for _, h := range []string{halName, hal2Name} {
			if err := tx.CreateHuman(ctx, store.HumanBinding{Identity: h, ClaimType: store.ClaimEmail, Value: h}, now); err != nil {
				return err
			}
		}
		if err := tx.CreateScope(ctx, "job1", now); err != nil {
			return err
		}
		return tx.CreateScopedIdentity(ctx, "job1-coder", "job1", now)
	})
}

func TestResolve(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s := openStore(t, e)
		seed(t, s)
		mustRead(t, s, func(ctx context.Context, tx store.Tx) error {
			for _, bad := range []string{"", store.Everyone, store.Admins, store.SlackBridge, "ghost"} {
				_, err := access.Resolve(ctx, tx, bad)
				var be *bus.Error
				if !errors.As(err, &be) || be.Code != bus.CodeNotRegistered || be.Retryable || be.Message != `"`+bad+`" is not a registered identity` {
					t.Errorf("Resolve(%q) = %v, want not_registered", bad, err)
				}
			}
			p, err := access.Resolve(ctx, tx, halName)
			if err != nil || p.Kind != store.IdentityHuman || p.Admin || p.Scope != "" {
				t.Errorf("human = %+v, %v", p, err)
			}
			p, err = access.Resolve(ctx, tx, "root")
			if err != nil || !p.Admin || p.Name != "root" {
				t.Errorf("root = %+v, %v", p, err)
			}
			p, err = access.Resolve(ctx, tx, "job1-coder")
			if err != nil || p.Scope != "job1" || p.Kind != store.IdentityAgent {
				t.Errorf("scoped = %+v, %v", p, err)
			}
			return nil
		})
	})
}

func mkChannel(t *testing.T, s store.Store, name string, kind store.Kind, creator, scope string, acl []store.ACLEntry) store.Channel {
	t.Helper()
	var ch store.Channel
	mustTx(t, s, func(ctx context.Context, tx store.Tx) error {
		seq, err := tx.MaxSeq(ctx)
		if err != nil {
			return err
		}
		ch, err = tx.CreateChannel(ctx, store.ChannelCreate{Name: name, Kind: kind, Creator: creator, Scope: scope, ACL: acl, CreatedSeq: seq, CreatedAt: now})
		return err
	})
	return ch
}

func TestRequireTexts(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s := openStore(t, e)
		seed(t, s)
		ch := mkChannel(t, s, "work", store.KindOrdinary, "", "", []store.ACLEntry{{Identity: store.Everyone, Permissions: []store.Permission{store.PermRead}}})
		mem := mkChannel(t, s, "mem", store.KindMemory, "", "", []store.ACLEntry{{Identity: store.Everyone, Permissions: []store.Permission{store.PermRead}}})
		mustTx(t, s, func(ctx context.Context, tx store.Tx) error {
			return tx.OpenSession(ctx, store.Session{ID: "s1", Identity: "kim", ResumeTokenHash: "h1", OpenedAt: now, LeaseExpiresAt: now + 1000})
		})
		type want struct {
			code      bus.Code
			text      string
			retryable bool
		}
		check := func(name string, who string, o access.Object, a access.Action, w *want, opts ...access.Option) {
			t.Helper()
			mustRead(t, s, func(ctx context.Context, tx store.Tx) error {
				p, err := access.Resolve(ctx, tx, who)
				if err != nil {
					t.Fatalf("%s: resolve: %v", name, err)
				}
				err = checker.Require(ctx, tx, p, o, a, opts...)
				if w == nil {
					if err != nil {
						t.Errorf("%s: %v, want allowed", name, err)
					}
					return nil
				}
				var be *bus.Error
				if !errors.As(err, &be) || be.Code != w.code || be.Message != w.text || be.Retryable != w.retryable {
					t.Errorf("%s: got %v, want %s %q", name, err, w.code, w.text)
				}
				return nil
			})
		}
		check("read ok", "pat", access.Channel{ID: ch.ID}, access.Read, nil)
		check("write denied", "pat", access.Channel{ID: ch.ID}, access.Write, &want{bus.CodeForbidden, `pat needs write on channel "work"`, false})
		check("admin denied", "pat", access.Channel{ID: ch.ID}, access.Admin, &want{bus.CodeForbidden, `pat needs admin on channel "work"`, false})
		check("admin by flag", "root", access.Channel{ID: ch.ID}, access.Admin, nil)
		check("server denied", "pat", access.Server{}, access.Administer, &want{bus.CodeForbidden, "administrator privilege required", false})
		check("server ok", "root", access.Server{}, access.Administer, nil)
		check("server with text", "pat", access.Server{}, access.Administer, &want{bus.CodeForbidden, "only an administrator may name a scope", false}, access.WithText("only an administrator may name a scope"))
		check("session other", "pat", access.Session{ID: "s1"}, access.Use, &want{bus.CodeNotFound, `session "s1" does not exist`, false})
		check("session missing", "pat", access.Session{ID: "nope"}, access.Use, &want{bus.CodeNotFound, `session "nope" does not exist`, false})
		check("session own", "kim", access.Session{ID: "s1"}, access.Use, nil)
		check("scope gate", "job1-coder", access.Channel{ID: ch.ID}, access.Read, &want{bus.CodeNotFound, `channel "work" does not exist`, false})
		check("scope gate keeps WithText out", "job1-coder", access.Channel{ID: ch.ID}, access.Read, &want{bus.CodeNotFound, `channel "work" does not exist`, false}, access.WithText("custom"))
		check("agent memory", "pat", access.Channel{ID: mem.ID}, access.Deliver, &want{bus.CodeValidation, "mem is a memory channel: agents do not subscribe to memories (decision 20); use search to find a memory and get_memory to read it", false})
		check("agent memory read", "pat", access.Channel{ID: mem.ID}, access.Read, nil)
		check("identity scope", "job1-coder", access.Identity{Name: "pat"}, access.See, &want{bus.CodeNotFound, `identity "pat" is not registered`, false})
		check("undefined pair", "pat", access.Server{}, access.Read, &want{bus.CodeInternal, `access: action "read" is not defined for server`, true})
		check("unknown channel", "pat", access.Channel{ID: 99999}, access.Read, &want{bus.CodeNotFound, "channel 99999 does not exist", false})
		// Check never panics on an undefined pair.
		mustRead(t, s, func(ctx context.Context, tx store.Tx) error {
			p, _ := access.Resolve(ctx, tx, "pat")
			d, err := checker.Check(ctx, tx, p, access.Session{ID: "s1"}, "bogus")
			if err != nil || d.Allowed || d.Code != bus.CodeInternal {
				t.Errorf("Check bogus = %+v, %v", d, err)
			}
			return nil
		})
	})
}

func TestChannelsAgreesWithCheck(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s := openStore(t, e)
		seed(t, s)
		seedVal := rand.Uint64()
		t.Logf("seed %d", seedVal)
		rng := rand.New(rand.NewPCG(seedVal, 1))
		names := []string{"pat", "kim", halName, "job1-coder", store.Everyone, store.Admins}
		creators := []string{"", "pat", "kim"}
		scopes := []string{"", "job1"}
		kinds := []store.Kind{store.KindOrdinary, store.KindMemory, store.KindTasks}
		perms := [][]store.Permission{
			{store.PermRead}, {store.PermWrite}, {store.PermAdmin},
			{store.PermRead, store.PermWrite}, {store.PermRead, store.PermAdmin},
			{store.PermWrite, store.PermAdmin}, {store.PermRead, store.PermWrite, store.PermAdmin},
		}
		for i := range 40 {
			var acl []store.ACLEntry
			for _, n := range names {
				if rng.IntN(3) == 0 {
					acl = append(acl, store.ACLEntry{Identity: n, Permissions: perms[rng.IntN(len(perms))]})
				}
			}
			if acl == nil {
				acl = []store.ACLEntry{}
			}
			mkChannel(t, s, "c"+string(rune('a'+i/26))+string(rune('a'+i%26)), kinds[rng.IntN(len(kinds))], creators[rng.IntN(len(creators))], scopes[rng.IntN(len(scopes))], acl)
		}
		actions := []access.Action{access.Read, access.Write, access.Admin, access.List, access.Deliver, access.Post}
		mustRead(t, s, func(ctx context.Context, tx store.Tx) error {
			for _, who := range []string{"root", "pat", "kim", halName, hal2Name, "job1-coder"} {
				p, err := access.Resolve(ctx, tx, who)
				if err != nil {
					t.Fatal(err)
				}
				chans, err := tx.ListChannels(ctx)
				if err != nil {
					t.Fatal(err)
				}
				for _, a := range actions {
					var want []int64
					for _, c := range chans {
						d, err := checker.Check(ctx, tx, p, access.Channel{ID: c.ID}, a)
						if err != nil {
							t.Fatal(err)
						}
						if d.Allowed {
							want = append(want, c.ID)
						}
					}
					slices.Sort(want)
					got, err := checker.Channels(ctx, tx, p, a)
					if err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(got, want) && (len(got) != 0 || len(want) != 0) {
						t.Errorf("%s %s: Channels = %v, Check says %v", who, a, got, want)
					}
				}
			}
			if _, err := checker.Channels(ctx, tx, access.Principal{Name: "pat"}, access.Administer); bus.CodeOf(err) != bus.CodeInternal {
				t.Errorf("Channels with a server action = %v, want internal", err)
			}
			return nil
		})
	})
}

func TestIdentitiesAgreesWithCheck(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s := openStore(t, e)
		seed(t, s)
		mustTx(t, s, func(ctx context.Context, tx store.Tx) error {
			return tx.CreateScopedIdentity(ctx, "job1-tester", "job1", now)
		})
		mustRead(t, s, func(ctx context.Context, tx store.Tx) error {
			all, err := tx.ListIdentities(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for _, who := range []string{"root", "pat", halName, "job1-coder"} {
				p, _ := access.Resolve(ctx, tx, who)
				var want []string
				for _, id := range all {
					d, err := checker.Check(ctx, tx, p, access.Identity{Name: id.Name}, access.See)
					if err != nil {
						t.Fatal(err)
					}
					if d.Allowed {
						want = append(want, id.Name)
					}
				}
				got, err := checker.Identities(ctx, tx, p)
				if err != nil {
					t.Fatal(err)
				}
				var names []string
				for _, id := range got {
					names = append(names, id.Name)
				}
				if !reflect.DeepEqual(names, want) {
					t.Errorf("%s: Identities = %v, Check says %v", who, names, want)
				}
				if who == "job1-coder" && !reflect.DeepEqual(names, []string{"job1-coder", "job1-tester"}) {
					t.Errorf("scoped listing = %v", names)
				}
				if who == "pat" && len(names) != len(all) {
					t.Errorf("unscoped listing = %v, want all %d", names, len(all))
				}
			}
			return nil
		})
	})
}

func TestHolders(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s := openStore(t, e)
		seed(t, s)
		w := []store.Permission{store.PermWrite}
		rd := []store.Permission{store.PermRead}
		ch := mkChannel(t, s, "bridged", store.KindOrdinary, "", "", []store.ACLEntry{
			{Identity: halName, Permissions: w}, {Identity: hal2Name, Permissions: rd}, {Identity: "pat", Permissions: w},
			{Identity: store.Everyone, Permissions: rd},
		})
		open := mkChannel(t, s, "open", store.KindOrdinary, "", "", []store.ACLEntry{{Identity: store.Everyone, Permissions: w}})
		mustRead(t, s, func(ctx context.Context, tx store.Tx) error {
			for _, c := range []store.Channel{ch, open} {
				ids, _ := tx.ListIdentities(ctx)
				var want []string
				for _, id := range ids {
					if id.Kind != store.IdentityHuman || store.ReservedIdentity(id.Name) {
						continue
					}
					p, _ := access.Resolve(ctx, tx, id.Name)
					if d, _ := checker.Check(ctx, tx, p, access.Channel{ID: c.ID}, access.Post); d.Allowed {
						want = append(want, id.Name)
					}
				}
				got, err := checker.Holders(ctx, tx, access.Channel{ID: c.ID}, access.Post)
				if err != nil {
					t.Fatal(err)
				}
				var names []string
				for _, p := range got {
					names = append(names, p.Name)
					if store.ReservedIdentity(p.Name) {
						t.Errorf("Holders listed reserved name %q", p.Name)
					}
				}
				if !reflect.DeepEqual(names, want) {
					t.Errorf("%s: Holders = %v, want %v", c.Name, names, want)
				}
				if c.ID == ch.ID && !reflect.DeepEqual(names, []string{halName}) {
					t.Errorf("bridged holders = %v, want only %s", names, halName)
				}
			}
			return nil
		})
	})
}
