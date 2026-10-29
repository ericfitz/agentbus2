package bus

import (
	"bytes"
	"context"
	"crypto"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ericfitz/agentbus2/internal/auth/assertion"
	"github.com/ericfitz/agentbus2/internal/auth/keys"
	"github.com/ericfitz/agentbus2/internal/auth/token"
	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/bus/remote"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/identities"
	"github.com/ericfitz/agentbus2/internal/core/maintenance"
	"github.com/ericfitz/agentbus2/internal/dbtest"
	"github.com/ericfitz/agentbus2/internal/gen"
	"github.com/ericfitz/agentbus2/internal/server"
	"github.com/ericfitz/agentbus2/internal/server/cmd"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
)

const replicaIssuer = "http://agentbus2.test"

// replicaListeningRE captures address, engine, registration mode, issuer and
// kid from `agentbus2 server listening on ADDR (ENGINE, registration MODE,
// issuer URL, kid KID)`. The issuer here has no port, unlike auth_test.go's.
var replicaListeningRE = regexp.MustCompile(`listening on (\S+) \((\w+), registration (\w+), issuer (\S+), kid (\S+)\)`)

// replica is one `agentbus2 server` started through cmd.Run.
type replica struct {
	name           string
	stdout, stderr syncBuffer
	done           chan int
	base           string // http://127.0.0.1:port
	mode, issuer   string
	kid            string
	client         *gen.ClientWithResponses
}

// startReplica runs the real server command over the database at dsn and
// waits for its listening line.
func startReplica(t *testing.T, ctx context.Context, name string, e dbtest.Engine, dsn string, extra ...string) *replica {
	t.Helper()
	r := &replica{name: name, done: make(chan int, 1)}
	storeFlag := "-sqlite"
	if e.Name == "postgres" {
		storeFlag = "-postgres"
	}
	args := []string{"-listen", "127.0.0.1:0", storeFlag, dsn, "-registration", "open",
		"-set", "issuer=" + replicaIssuer, "-set", "watch_merge_seconds=1", "-set", "watch_max_seconds=5"}
	args = append(args, extra...)
	go func() { r.done <- cmd.Run(ctx, args, nil, &r.stdout, &r.stderr) }()
	deadline := time.Now().Add(30 * time.Second)
	for {
		if m := replicaListeningRE.FindStringSubmatch(r.stdout.String()); m != nil {
			r.base = "http://" + m[1]
			r.mode, r.issuer, r.kid = m[3], m[4], m[5]
			break
		}
		select {
		case code := <-r.done:
			t.Fatalf("replica %s exited %d before listening; stdout %q stderr %q", name, code, r.stdout.String(), r.stderr.String())
		case <-time.After(20 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			t.Fatalf("replica %s: no listening line; stdout %q stderr %q", name, r.stdout.String(), r.stderr.String())
		}
	}
	var err error
	if r.client, err = gen.NewClientWithResponses(r.base); err != nil {
		t.Fatal(err)
	}
	return r
}

// clientWith is a client for r carrying bearer.
func (r *replica) clientWith(t *testing.T, bearer string) *gen.ClientWithResponses {
	t.Helper()
	c, err := gen.NewClientWithResponses(r.base, gen.WithRequestEditorFn(remote.WithBearer(bearer)))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// replicaWho is an identity of the replicas test.
type replicaWho struct {
	name string
	key  crypto.Signer
}

func newReplicaWho(t *testing.T, name string) *replicaWho {
	t.Helper()
	k, err := keys.Generate(keys.EdDSA)
	if err != nil {
		t.Fatal(err)
	}
	return &replicaWho{name: name, key: k}
}

// assertionFor signs a fresh assertion for the shared issuer's token endpoint.
func (w *replicaWho) assertionFor(t *testing.T) string {
	t.Helper()
	kid, err := keys.KID(w.key.Public())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := assertion.Sign(assertion.Input{Identity: w.name, Audience: replicaIssuer, Key: w.key, KID: kid, Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// register registers w through r (registration is open).
func (r *replica) register(t *testing.T, ctx context.Context, w *replicaWho) {
	t.Helper()
	res, err := remote.Register(ctx, r.client, w.name, w.key.Public(), "")
	if err != nil || !res.Created {
		t.Fatalf("Register %s through %s = %+v, %v", w.name, r.name, res, err)
	}
}

// login exchanges a fresh assertion of w through r; any replica accepts the
// bearer.
func (r *replica) login(t *testing.T, ctx context.Context, w *replicaWho) (string, *remote.Bus) {
	t.Helper()
	at, err := remote.Token(ctx, r.client, w.assertionFor(t))
	if err != nil {
		t.Fatalf("token for %s through %s: %v", w.name, r.name, err)
	}
	return at.Token, remote.New(r.clientWith(t, at.Token))
}

// waitWatchRegistered blocks until a watch has registered on the session (its
// watch id is set and differs from prev, the id seen before; "" for none) and
// returns that id. A fixed sleep alone is not enough: a loaded host can run
// the send before the Watch request reaches the server, and the watch's first
// peek then finds the message and returns at once, which passes the wake leaf
// even with the NOTIFY removed (scripts/check-gates-selftest.sh block
// notify-append-removed flaked on exactly this). The caller still sleeps a
// beat after this so the watch is past its first peek and waiting on hints.
func waitWatchRegistered(t *testing.T, ctx context.Context, st ab2store.Store, session, prev string) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var id string
		err := st.Read(ctx, func(ctx context.Context, tx ab2store.Tx) error {
			sess, err := tx.Session(ctx, session)
			if err == nil {
				id = sess.WatchID
			}
			return err
		})
		if err != nil {
			t.Fatalf("reading session %s: %v", session, err)
		}
		if id != "" && id != prev {
			return id
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no new watch registered on session %s within 10 s", session)
	return ""
}

// watchOutcome is the result of a watch armed in a goroutine.
type watchOutcome struct {
	res ab2bus.WatchResult
	err error
	at  time.Time
}

// TestReplicasWake is the Milestone 7 exit criterion (spec section 10): two
// `agentbus2 server` replicas over one database deliver and wake correctly.
// The leaves that need a NOTIFY (3 and 5) exist on postgres only: two servers
// over one SQLite file share no hints (Milestone 4 choice 14), so on sqlite
// they could only wait for nothing to happen, which no defect can fail. That
// limitation stays pinned by Milestone 4's TestWatchWakesAcrossProcesses and
// TestWatchReplacedAcrossProcesses; local multi-process mode is the Milestone
// 6 client's polling. Block notify-append-removed of
// scripts/check-gates-selftest.sh breaks this test.
func TestReplicasWake(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		st, _, dsn := openStoreDSN(t, e)

		a := startReplica(t, ctx, "A", e, dsn)
		b := startReplica(t, ctx, "B", e, dsn)
		stopped := false
		stop := func() {
			if stopped {
				return
			}
			stopped = true
			cancel()
			for _, r := range []*replica{a, b} {
				select {
				case code := <-r.done:
					if code != 0 {
						t.Errorf("replica %s: Run returned %d after cancel, want 0; stderr %q", r.name, code, r.stderr.String())
					}
				case <-time.After(5 * time.Second):
					t.Errorf("replica %s: Run did not return within 5 s of cancel", r.name)
				}
			}
		}
		defer stop()

		for _, r := range []*replica{a, b} {
			if r.mode != "open" || r.issuer != replicaIssuer {
				t.Errorf("replica %s: registration %q issuer %q, want open and %s", r.name, r.mode, r.issuer, replicaIssuer)
			}
		}
		if a.kid != b.kid {
			t.Errorf("kid differs: A %s, B %s", a.kid, b.kid)
		}
		if n := strings.Count(a.stdout.String()+b.stdout.String(), "generated signing key"); n != 1 {
			t.Errorf("%d replicas generated a signing key, want exactly 1:\nA: %s\nB: %s", n, a.stdout.String(), b.stdout.String())
		}
		if t.Failed() {
			t.FailNow()
		}

		pat := newReplicaWho(t, "pat")
		sam := newReplicaWho(t, "sam")
		var patBusA, patBusB *remote.Bus

		// (1) The signing key row and the issuer are shared (choice 8).
		t.Run("shared-key-and-issuer", func(t *testing.T) {
			a.register(t, ctx, pat)
			var bearer string
			bearer, patBusA = a.login(t, ctx, pat)
			status, body := getChannels(t, b.base, bearer)
			if status != http.StatusOK {
				t.Fatalf("GET /v1/channels through B with A's bearer = %d %s, want 200", status, body)
			}
			patBusB = remote.New(b.clientWith(t, bearer))
		})
		if patBusB == nil {
			t.Fatal("pat has no bearer; the remaining subtests cannot run")
		}

		// (1b) An assertion is single-use across replicas (Task 9; Decision
		// (Eric, 2026-10-21), item 4), through the shared assertion_jti table.
		t.Run("assertion-single-use-across-replicas", func(t *testing.T) {
			raw := pat.assertionFor(t)
			if _, err := remote.Token(ctx, a.client, raw); err != nil {
				t.Fatalf("first presentation through A: %v", err)
			}
			_, err := remote.Token(ctx, b.client, raw)
			if err == nil {
				t.Fatal("the same assertion through B was accepted, want 401 assertion replayed")
			}
			codeOf(t, err, ab2bus.CodeUnauthenticated)
			if !strings.Contains(err.Error(), "assertion replayed") {
				t.Fatalf("second presentation through B: %v, want detail containing %q", err, "assertion replayed")
			}
			if _, err := remote.Token(ctx, b.client, pat.assertionFor(t)); err != nil {
				t.Fatalf("a fresh assertion through B: %v", err)
			}
		})

		// (2) A subscription made through one replica is honored by the other.
		var session ab2bus.Session
		t.Run("subscribe-through-b", func(t *testing.T) {
			if _, err := patBusB.CreateChannel(ctx, ab2bus.CreateChannelInput{Name: "dev"}); err != nil {
				t.Fatalf("CreateChannel through B: %v", err)
			}
			if err := patBusB.Subscribe(ctx, ab2bus.SubscribeInput{Channel: "dev"}); err != nil {
				t.Fatalf("Subscribe through B: %v", err)
			}
			var err error
			if session, err = patBusA.Open(ctx, ab2bus.OpenSessionInput{Hint: "pat"}); err != nil {
				t.Fatalf("Open through A: %v", err)
			}
		})
		if session.ID == "" {
			t.Fatal("pat has no session; the remaining subtests cannot run")
		}

		b.register(t, ctx, sam)
		_, samBusB := b.login(t, ctx, sam)
		send := func(content string) time.Time {
			t.Helper()
			at := time.Now()
			if _, err := samBusB.Send(ctx, ab2bus.SendInput{Channel: "dev", Content: content}); err != nil {
				t.Fatalf("sam Send through B: %v", err)
			}
			return at
		}

		// (3) A message sent through B wakes a watch armed on A within the
		// merge window: the NOTIFY crossed the servers. A timeout (5 s) is
		// distinguishable from a wake by the bound.
		var firstWatch string
		if e.Name == "postgres" {
			t.Run("wake-crosses-servers", func(t *testing.T) {
				out := make(chan watchOutcome, 1)
				go func() {
					res, err := patBusA.Watch(ctx, ab2bus.WatchInput{Session: session.ID, Timeout: 5 * time.Second})
					out <- watchOutcome{res, err, time.Now()}
				}()
				firstWatch = waitWatchRegistered(t, ctx, st, session.ID, "")
				time.Sleep(300 * time.Millisecond)
				sent := send("hello")
				select {
				case o := <-out:
					if o.err != nil {
						t.Fatalf("watch on A: %v", o.err)
					}
					if !o.res.Woke || o.res.Count != 1 || fmt.Sprint(o.res.Channels) != "[dev]" {
						t.Fatalf("watch on A = %+v, want woke count 1 channels [dev]", o.res)
					}
					// 4 s, not watch_merge_seconds + 1 s: loaded Windows runners
					// woke 1.6 s late elsewhere. Still under the 5 s watch timeout
					// (= watch_max_seconds), so a timeout cannot pass.
					if d := o.at.Sub(sent); d > 4*time.Second {
						t.Fatalf("watch on A woke %v after the send, want within 4 s", d)
					}
				case <-time.After(10 * time.Second):
					t.Fatal("watch on A did not return")
				}
			})
		} else {
			send("hello") // sqlite has no wake leaf; leaf 4 still has its message
		}

		// (4) Delivery goes through the shared database: the cursor and the
		// acked mark live there, not in either process.
		t.Run("deliver-crosses-servers", func(t *testing.T) {
			got, err := patBusB.Receive(ctx, ab2bus.ReceiveInput{Session: session.ID})
			if err != nil {
				t.Fatalf("Receive through B: %v", err)
			}
			if len(got.Messages) != 1 || got.Messages[0].Content != "hello" || got.Batch == "" {
				t.Fatalf("Receive through B = %+v, want sam's one message and a batch token", got)
			}
			next, err := patBusB.Receive(ctx, ab2bus.ReceiveInput{Session: session.ID, Ack: got.Batch})
			if err != nil || next.AckIgnored || len(next.Messages) != 0 {
				t.Fatalf("ack through B = %+v, %v; want the ack honored and an empty batch", next, err)
			}
			again, err := patBusA.Receive(ctx, ab2bus.ReceiveInput{Session: session.ID})
			if err != nil || len(again.Messages) != 0 {
				t.Fatalf("Receive through A after the ack = %+v, %v; want an empty batch", again, err)
			}
		})

		// (5) A watch armed on B replaces the one armed on A (Gate 0 item 1,
		// Decision (Eric, 2026-10-21)); the control NOTIFY crosses servers.
		if e.Name == "postgres" {
			t.Run("watch-replaced-across-servers", func(t *testing.T) {
				out := make(chan watchOutcome, 1)
				go func() {
					res, err := patBusA.Watch(ctx, ab2bus.WatchInput{Session: session.ID, Timeout: 5 * time.Second})
					out <- watchOutcome{res, err, time.Now()}
				}()
				waitWatchRegistered(t, ctx, st, session.ID, firstWatch)
				time.Sleep(300 * time.Millisecond)
				armed := time.Now()
				go func() { _, _ = patBusB.Watch(ctx, ab2bus.WatchInput{Session: session.ID, Timeout: time.Second}) }()
				select {
				case o := <-out:
					codeOf(t, o.err, ab2bus.CodeReplaced)
					// 3 s, not 1.5: loaded Windows runners. A's watch runs to
					// its 5 s timeout unless the control NOTIFY arrives.
					if d := o.at.Sub(armed); d > 3*time.Second {
						t.Fatalf("A's watch was replaced %v after B armed, want within 3 s", d)
					}
				case <-time.After(12 * time.Second):
					t.Fatal("A's watch did not return")
				}
			})
		}

		stop()
	})
}

// replicaTick is one OnTick call of a replica.
type replicaTick struct {
	replica int
	ran     bool
	jobs    []string // every job the tick reported, run or skipped
	done    []string // the jobs that ran to completion without an error
}

// serveInstance is one cmd.Serve of TestReplicasMaintenanceExclusive.
type serveInstance struct {
	cancel context.CancelFunc
	errc   chan error
}

// TestReplicasMaintenanceExclusive: two Serve instances over one database,
// started together, and exactly one runs the maintenance jobs (Decisions 49
// and 68); when it stops, the other takes over. Blocks advisory-lock-ignored
// and sqlite-lease-ignored of scripts/check-gates-selftest.sh break it.
func TestReplicasMaintenanceExclusive(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s1, reopen, dsn := openStoreDSN(t, e)
		s2, err := reopen()
		if err != nil {
			t.Fatalf("reopen: %v", err)
		}

		cfg := core.Defaults()
		cfg.MaintenanceIntervalSeconds = 1
		probe, err := core.New(s1, cfg, core.Options{Now: time.Now})
		if err != nil {
			t.Fatal(err)
		}
		var jobNames []string
		for _, j := range maintenance.New(probe).Jobs() {
			jobNames = append(jobNames, j.Name)
		}

		var mu sync.Mutex
		var log []replicaTick
		onTick := func(n int) func(bool, []maintenance.JobResult) {
			return func(ran bool, rep []maintenance.JobResult) {
				tk := replicaTick{replica: n, ran: ran}
				for _, r := range rep {
					tk.jobs = append(tk.jobs, r.Name)
					if !r.Skipped && r.Err == nil {
						tk.done = append(tk.done, r.Name)
					}
				}
				mu.Lock()
				log = append(log, tk)
				mu.Unlock()
			}
		}

		start := make(chan struct{})
		run := func(n int, st ab2store.Store) serveInstance {
			ctx, cancel := context.WithCancel(context.Background())
			in := serveInstance{cancel: cancel, errc: make(chan error, 1)}
			c := cfg
			sc := cmd.Config{
				Listen: "127.0.0.1:0", Cfg: &c, Auth: server.RefuseAll{}, Registration: identities.ModeOpen,
				Store: st, Notifier: crossProcessNotifier(t, e, dsn), OnTick: onTick(n),
			}
			go func() { <-start; in.errc <- cmd.Serve(ctx, sc) }()
			return in
		}
		r1, r2 := run(1, s1), run(2, s2)
		defer r1.cancel()
		defer r2.cancel()
		close(start)

		// names[r] is every job a tick of replica r reported, whatever its
		// ran flag says (a non-holder must report none); done[r] the jobs
		// that ran without an error.
		snapshot := func() (ranBy, idle map[int]int, names, done map[int]map[string]bool) {
			mu.Lock()
			defer mu.Unlock()
			ranBy, idle = map[int]int{}, map[int]int{}
			names = map[int]map[string]bool{1: {}, 2: {}}
			done = map[int]map[string]bool{1: {}, 2: {}}
			for _, tk := range log {
				if tk.ran {
					ranBy[tk.replica]++
				} else {
					idle[tk.replica]++
				}
				for _, j := range tk.jobs {
					names[tk.replica][j] = true
				}
				for _, j := range tk.done {
					done[tk.replica][j] = true
				}
			}
			return
		}

		var holder, other int
		var holderRun, otherRun serveInstance
		t.Run("one-holder", func(t *testing.T) {
			time.Sleep(4 * time.Second)
			ranBy, idle, names, done := snapshot()
			switch {
			case ranBy[1] > 0 && ranBy[2] > 0:
				t.Fatalf("both replicas ran the jobs: ran %v idle %v", ranBy, idle)
			case ranBy[1] > 0:
				holder, other, holderRun, otherRun = 1, 2, r1, r2
			case ranBy[2] > 0:
				holder, other, holderRun, otherRun = 2, 1, r2, r1
			default:
				t.Fatalf("neither replica ran the jobs: idle %v", idle)
			}
			if ranBy[holder] < 2 {
				t.Errorf("holder %d ran %d ticks, want at least 2", holder, ranBy[holder])
			}
			for _, name := range jobNames {
				if !done[holder][name] {
					t.Errorf("job %q ran to completion in no tick of replica %d", name, holder)
				}
			}
			if len(names[other]) != 0 || idle[other] == 0 {
				t.Errorf("replica %d: want only idle ticks and at least one, got idle %d, jobs %v", other, idle[other], names[other])
			}
			var chans []ab2store.Channel
			if err := s1.Tx(context.Background(), func(ctx context.Context, tx ab2store.Tx) error {
				var err error
				chans, err = tx.ListChannels(ctx)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			general := 0
			for _, c := range chans {
				if c.Name == "general" {
					general++
				}
			}
			if general != 1 {
				t.Errorf("%d general channels after the simultaneous start, want 1", general)
			}
			// Losing the start race (EnsureGeneral, the listener) would end
			// a Serve early; both must still be serving.
			for n, in := range map[int]serveInstance{1: r1, 2: r2} {
				select {
				case err := <-in.errc:
					t.Fatalf("replica %d's Serve returned during the start: %v", n, err)
				default:
				}
			}
		})
		if holder == 0 {
			t.Fatal("no holder; failover cannot run")
		}

		t.Run("failover", func(t *testing.T) {
			holderRun.cancel()
			select {
			case err := <-holderRun.errc:
				if err != nil {
					t.Fatalf("holder's Serve after cancel: %v", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("holder's Serve did not return")
			}
			ranByOther := func() int { r, _, _, _ := snapshot(); return r[other] }
			before := ranByOther()
			waitFor(t, 3*time.Second, fmt.Sprintf("replica %d to take over", other), func() bool { return ranByOther() > before })
			otherRun.cancel()
			select {
			case <-otherRun.errc:
			case <-time.After(10 * time.Second):
				t.Fatal("the other Serve did not return")
			}
		})
	})
}

// bearerKID is the kid in the header of a compact JWT.
func bearerKID(t *testing.T, raw string) string {
	t.Helper()
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		t.Fatalf("bearer has %d parts, want 3", len(parts))
	}
	hdr, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatal(err)
	}
	var h struct {
		KID string `json:"kid"`
	}
	if err := json.Unmarshal(hdr, &h); err != nil {
		t.Fatal(err)
	}
	return h.KID
}

// TestReplicasSigningKeyRotation: two replicas over one database and
// rotations of the signing key set (issue #6 acceptance 4). The replicas and
// the host commands run with signing_key_rotation_cooldown_seconds=1 (the
// default 3600 s would refuse a rotation right after the key's creation) and
// each leaf waits past that second before it rotates.
func TestReplicasSigningKeyRotation(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		_, _, dsn := openStoreDSN(t, e)
		const cooldown = "signing_key_rotation_cooldown_seconds=1"
		a := startReplica(t, ctx, "A", e, dsn, "-set", cooldown)
		b := startReplica(t, ctx, "B", e, dsn, "-set", cooldown)
		stopped := false
		stop := func() {
			if stopped {
				return
			}
			stopped = true
			cancel()
			for _, r := range []*replica{a, b} {
				select {
				case code := <-r.done:
					if code != 0 {
						t.Errorf("replica %s: Run returned %d after cancel, want 0; stderr %q", r.name, code, r.stderr.String())
					}
				case <-time.After(5 * time.Second):
					t.Errorf("replica %s: Run did not return within 5 s of cancel", r.name)
				}
			}
		}
		defer stop()

		storeFlag := "-sqlite"
		if e.Name == "postgres" {
			storeFlag = "-postgres"
		}
		adminWith := func(cd string, args ...string) (int, string, string) {
			var so, se bytes.Buffer
			code := cmd.RunAdmin(ctx, append([]string{storeFlag, dsn, "-set", cd}, args...), nil, &so, &se)
			return code, so.String(), se.String()
		}
		admin := func(args ...string) (int, string, string) { return adminWith(cooldown, args...) }
		pastCooldown := func() { time.Sleep(1200 * time.Millisecond) }

		// (1) Two host rotations at once: one wins, one meets the cooldown.
		t.Run("concurrent-rotate-one-active", func(t *testing.T) {
			// The host commands use a 3 s cooldown here and the key is older
			// than that, so both may rotate; a loser that stalls up to 3 s
			// after the winner still meets the winner's young key.
			const wide = "signing_key_rotation_cooldown_seconds=3"
			time.Sleep(3200 * time.Millisecond)
			var codes [2]int
			var errs [2]string
			var wg sync.WaitGroup
			for i := range codes {
				wg.Add(1)
				go func() {
					defer wg.Done()
					codes[i], _, errs[i] = adminWith(wide, "signing-key", "rotate")
				}()
			}
			wg.Wait()
			zero, one := 0, 0
			for i, c := range codes {
				switch c {
				case 0:
					zero++
				case 1:
					one++
					if !strings.Contains(errs[i], "the next rotation is allowed at") {
						t.Errorf("the losing rotation's stderr = %q, want the cooldown text", errs[i])
					}
				}
			}
			if zero != 1 || one != 1 {
				t.Fatalf("exit codes %v (stderr %q, %q), want exactly one 0 and one 1", codes, errs[0], errs[1])
			}
			code, out, se := admin("signing-key", "list")
			if code != 0 {
				t.Fatalf("list: %d %s", code, se)
			}
			if n := strings.Count(out, " active "); n != 1 || strings.Count(out, " retiring ") != 1 {
				t.Fatalf("list after the race:\n%s\nwant one active and one retiring key", out)
			}
		})

		root := newReplicaWho(t, "root")
		a.register(t, ctx, root)
		if code, so, se := admin("admins", "add", "-identity", "root"); code != 0 {
			t.Fatalf("admins add: %d %s%s", code, so, se)
		}
		_, rootBusA := a.login(t, ctx, root)

		// (2) A rotation through A's API: A mints with the new kid, and B
		// (which has never seen it) accepts the bearer at once.
		var bearer2 string
		t.Run("new-kid-verifies-on-both", func(t *testing.T) {
			pastCooldown()
			rot, err := rootBusA.RotateSigningKey(ctx, ab2bus.RotateSigningKeyInput{})
			if err != nil {
				t.Fatalf("RotateSigningKey through A: %v", err)
			}
			bearer2, _ = a.login(t, ctx, root)
			if got := bearerKID(t, bearer2); got != rot.Active.KID {
				t.Fatalf("A minted with kid %s, want the new active %s", got, rot.Active.KID)
			}
			if status, body := getChannels(t, a.base, bearer2); status != http.StatusOK {
				t.Fatalf("GET /v1/channels through A = %d %s, want 200", status, body)
			}
			if status, body := getChannels(t, b.base, bearer2); status != http.StatusOK {
				t.Fatalf("GET /v1/channels through B with the new kid's bearer = %d %s, want 200 (unknown-kid reload)", status, body)
			}
		})
		if bearer2 == "" {
			t.Fatal("no bearer from the new key; the revoke leaf cannot run")
		}

		// (3) A revoking rotation: A refuses the revoked key's bearer at once,
		// B within one reload interval plus the unknown-kid margin.
		t.Run("revoke-propagates", func(t *testing.T) {
			// open note: signing-keys 9
			rootA2 := remote.New(a.clientWith(t, bearer2))
			if _, err := rootA2.RotateSigningKey(ctx, ab2bus.RotateSigningKeyInput{RevokePrevious: true}); err != nil {
				t.Fatalf("revoking rotation through A: %v", err)
			}
			if status, body := getChannels(t, a.base, bearer2); status != http.StatusUnauthorized {
				t.Fatalf("A with the revoked key's bearer = %d %s, want 401 at once", status, body)
			}
			deadline := time.Now().Add(token.ReloadInterval + 5*time.Second)
			for {
				status, body := getChannels(t, b.base, bearer2)
				if status == http.StatusUnauthorized {
					return
				}
				if time.Now().After(deadline) {
					t.Fatalf("B still accepts the revoked key's bearer after %s: %d %s", token.ReloadInterval+5*time.Second, status, body)
				}
				time.Sleep(500 * time.Millisecond)
			}
		})
		stop()
	})
}

// TestReplicasInspection is the issue #5 replica check: two `agentbus2 server`
// replicas set from inspection_endpoint each call the one inspector for their
// own writes; a refusal is a 422 inspection_rejected and stores nothing; a
// failing inspector makes a send retryable unavailable and health degraded. It
// has no t.Run leaf, so the counted Milestone 7 leaves do not change.
func TestReplicasInspection(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		_, _, dsn := openStoreDSN(t, e)

		var requests, failing atomic.Int64
		insp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests.Add(1)
			if failing.Load() != 0 {
				http.Error(w, "down", http.StatusInternalServerError)
				return
			}
			var req struct {
				Record struct {
					Content string `json:"content"`
				} `json:"record"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			if req.Record.Content == "no" {
				_, _ = w.Write([]byte(`{"verdict":"refuse","code":"blocked","reason":"not allowed"}`))
				return
			}
			_, _ = w.Write([]byte(`{"verdict":"allow"}`))
		}))
		defer insp.Close()

		set := []string{"-set", "inspection_endpoint=" + insp.URL}
		a := startReplica(t, ctx, "A", e, dsn, set...)
		b := startReplica(t, ctx, "B", e, dsn, set...)
		defer func() {
			cancel()
			for _, r := range []*replica{a, b} {
				select {
				case <-r.done:
				case <-time.After(5 * time.Second):
					t.Errorf("replica %s: Run did not return within 5 s of cancel", r.name)
				}
			}
		}()

		pat := newReplicaWho(t, "pat")
		a.register(t, ctx, pat)
		bearer, busA := a.login(t, ctx, pat)
		clientB := b.clientWith(t, bearer)
		busB := remote.New(clientB)
		if _, err := busA.CreateChannel(ctx, ab2bus.CreateChannelInput{Name: "dev"}); err != nil {
			t.Fatalf("CreateChannel: %v", err)
		}
		count := func() int {
			t.Helper()
			msgs, err := busA.History(ctx, ab2bus.HistoryInput{Channel: "dev", Count: 50})
			if err != nil {
				t.Fatalf("History: %v", err)
			}
			return len(msgs)
		}

		// Each replica calls the inspector once for its own send.
		for _, s := range []struct {
			who string
			bus *remote.Bus
		}{{"A", busA}, {"B", busB}} {
			if _, err := s.bus.Send(ctx, ab2bus.SendInput{Channel: "dev", Content: "yes"}); err != nil {
				t.Fatalf("Send through %s: %v", s.who, err)
			}
		}
		if n := requests.Load(); n != 2 {
			t.Fatalf("inspector saw %d requests for two sends, want 2", n)
		}
		if n := count(); n != 2 {
			t.Fatalf("%d messages after two allowed sends, want 2", n)
		}

		// A refusal through the second replica: inspection_rejected, 422, stored nothing.
		_, err := busB.Send(ctx, ab2bus.SendInput{Channel: "dev", Content: "no"})
		codeOf(t, err, ab2bus.CodeInspectionRejected)
		var be *ab2bus.Error
		if !errors.As(err, &be) || be.Retryable || !strings.Contains(be.Message, "rejected by inspection (blocked): not allowed") {
			t.Fatalf("refusal = %v", err)
		}
		resp, err := clientB.SendMessageWithResponse(ctx, "dev", &gen.SendMessageParams{}, gen.SendRequest{Content: "no"})
		if err != nil {
			t.Fatalf("raw send: %v", err)
		}
		if resp.StatusCode() != http.StatusUnprocessableEntity {
			t.Fatalf("raw refused send status %d, want 422; body %s", resp.StatusCode(), resp.Body)
		}
		if n := requests.Load(); n != 4 {
			t.Fatalf("inspector saw %d requests, want 4", n)
		}
		if n := count(); n != 2 {
			t.Fatalf("%d messages after refusals, want 2", n)
		}
		if h, err := busA.Health(ctx); err != nil || h.Status != ab2bus.HealthOK {
			t.Fatalf("health before the inspector fails = %+v, %v, want ok", h, err)
		}

		// A failing inspector closes writes (retryable) and degrades health.
		failing.Store(1)
		_, err = busA.Send(ctx, ab2bus.SendInput{Channel: "dev", Content: "yes"})
		codeOf(t, err, ab2bus.CodeUnavailable)
		if !errors.As(err, &be) || !be.Retryable {
			t.Fatalf("failed inspection = %v, want retryable", err)
		}
		if n := count(); n != 2 {
			t.Fatalf("%d messages after a failed inspection, want 2", n)
		}
		for _, bus := range []*remote.Bus{busA, busB} {
			if h, err := bus.Health(ctx); err != nil || h.Status != ab2bus.HealthDegraded || h.Tier != ab2bus.TierIdentity {
				t.Fatalf("health with a failing inspector = %+v, %v, want degraded at the identity tier", h, err)
			}
		}
	})
}
