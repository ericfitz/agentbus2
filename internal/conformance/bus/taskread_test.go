package bus

import (
	"context"
	"testing"
	"time"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/dbtest"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
)

// open note: postgres-budget 3. task_get and task_list record a read in
// channels.last_read_at, at most once an hour; no other read does.
func TestTaskReadMarksChannel(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		h.session("pat", "pat")
		todo := h.channel("todo", ab2store.KindTasks, "pat", []ab2store.ACLEntry{
			{Identity: "pat", Permissions: []ab2store.Permission{ab2store.PermRead, ab2store.PermWrite, ab2store.PermAdmin}},
		})
		pat := h.as("pat")
		made, err := pat.Create(ctx, ab2bus.TaskCreateInput{Channel: "todo", Subject: "first"})
		if err != nil {
			t.Fatal(err)
		}
		if got := h.channelRow(todo).LastReadAt; got != 0 {
			t.Fatalf("LastReadAt after create = %d, want 0", got)
		}

		h.clock.Advance(2 * time.Hour)
		if _, err := pat.List(ctx, ab2bus.TaskListInput{Channel: "todo"}); err != nil {
			t.Fatal(err)
		}
		first := h.core.NowMs()
		if got := h.channelRow(todo).LastReadAt; got != first {
			t.Fatalf("LastReadAt after List = %d, want %d", got, first)
		}

		h.clock.Advance(10 * time.Minute)
		if _, err := pat.List(ctx, ab2bus.TaskListInput{Channel: "todo"}); err != nil {
			t.Fatal(err)
		}
		if got := h.channelRow(todo).LastReadAt; got != first {
			t.Fatalf("LastReadAt within the hour = %d, want unchanged %d", got, first)
		}

		h.clock.Advance(61 * time.Minute)
		if _, err := pat.GetTask(ctx, made.ID); err != nil {
			t.Fatal(err)
		}
		second := h.core.NowMs()
		if got := h.channelRow(todo).LastReadAt; got != second {
			t.Fatalf("LastReadAt after Get = %d, want %d", got, second)
		}

		// A refused read does not move the mark.
		h.clock.Advance(2 * time.Hour)
		h.ensureIdentity("kim")
		kim := h.as("kim")
		_, err = kim.List(ctx, ab2bus.TaskListInput{Channel: "todo"})
		codeOf(t, err, ab2bus.CodeForbidden)
		_, err = kim.GetTask(ctx, made.ID)
		codeOf(t, err, ab2bus.CodeForbidden)
		if got := h.channelRow(todo).LastReadAt; got != second {
			t.Fatalf("LastReadAt after refused reads = %d, want %d", got, second)
		}

		// Receive and History do not count.
		b, s := recvOpen(h, "pat", "todo")
		recv(t, b, ab2bus.ReceiveInput{Session: s.ID, Channels: []string{"todo"}})
		if _, err := pat.History(ctx, ab2bus.HistoryInput{Channel: "todo"}); err != nil {
			t.Fatal(err)
		}
		if got := h.channelRow(todo).LastReadAt; got != second {
			t.Fatalf("LastReadAt after Receive and History = %d, want %d", got, second)
		}
	})
}
