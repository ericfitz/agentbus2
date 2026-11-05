package slackbridge

import (
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite the goldens in testdata/canvas")

var canvasAt = time.Date(2026, 11, 4, 20, 45, 0, 0, time.UTC)

func snap(lists []CanvasTaskList, mems []CanvasMemoryChannel) CanvasSnapshot {
	return CanvasSnapshot{ScopeName: "job1", At: canvasAt, TaskLists: lists, MemoryChannels: mems}
}

func ostat(tag, text, problem string) *CanvasOwnerStatus {
	return &CanvasOwnerStatus{Tag: tag, Text: text, Problem: problem}
}

func tsk(id int64, subject, status, assignee string, depth int) CanvasTask {
	return CanvasTask{ID: id, Subject: subject, Status: status, Assignee: assignee, Depth: depth}
}

func canvasCases() map[string]CanvasSnapshot {
	lease := time.Date(2026, 11, 4, 20, 41, 0, 0, time.UTC)

	bothBlockedHuman := tsk(1, "Ship release", "in_progress", "claude-a", 0)
	bothBlockedHuman.OpenBlockers = []int64{7, 8}
	bothBlockedHuman.OwnerStatus = ostat("needs-human", "which style?", "")
	stalled := tsk(2, "Fuzz the tokenizer", "in_progress", "claude-b", 0)
	stalled.Stalled, stalled.LeaseExpiredAt = true, lease
	problem := tsk(3, "Run e2e", "in_progress", "claude-c", 0)
	problem.OwnerStatus = ostat("working", "running", "docker daemon down")
	offline := tsk(4, "Index", "in_progress", "claude-d", 0)
	offline.Stalled = true
	attention := snap([]CanvasTaskList{
		{Channel: "job1-tasks-2", Tasks: []CanvasTask{offline, tsk(5, "Docs", "pending", "", 0)}},
		{Channel: "job1-tasks", Tasks: []CanvasTask{bothBlockedHuman, stalled, problem}},
	}, nil)

	root := tsk(1, "Wire the parser", "in_progress", "claude-a", 0)
	root.OwnerStatus = ostat("working", "parsing", "")
	st := tsk(3, "Fuzz the tokenizer", "in_progress", "claude-b", 1)
	st.Stalled, st.LeaseExpiredAt = true, lease
	gst := tsk(4, "Seed corpus", "in_progress", "", 2)
	gst.Stalled = true
	nh := tsk(5, "Pick the error format", "in_progress", "claude-a", 1)
	nh.OwnerStatus = ostat("needs-human", "which style?", "")
	blocked := tsk(6, "Ship release", "pending", "", 0)
	blocked.OpenBlockers = []int64{1}
	long := tsk(8, "Long problem", "in_progress", "claude-e", 0)
	long.OwnerStatus = ostat("working", "x", strings.Repeat("p", 130))
	tree := snap([]CanvasTaskList{{Channel: "job1-tasks", Tasks: []CanvasTask{
		root, tsk(2, "Define grammar", "completed", "claude-a", 1), st, gst,
		tsk(9, "Minimizer", "completed", "claude-b", 2), nh,
		blocked, tsk(7, "Write docs", "pending", "", 1), long,
	}}}, nil)

	doneHuman := tsk(3, "Done anyway", "completed", "claude-a", 0)
	doneHuman.OwnerStatus = ostat("needs-human", "stale", "stale problem")
	completed := snap([]CanvasTaskList{{Channel: "job1-tasks", Tasks: []CanvasTask{
		tsk(1, "Old root", "completed", "claude-a", 0), tsk(2, "Old child", "completed", "claude-a", 1),
		tsk(4, "Old grandchild", "completed", "", 2), doneHuman,
	}}}, nil)

	mem := func(id int64, subj, typ, content string, tags []string, day int) CanvasMemory {
		return CanvasMemory{ID: id, Subject: subj, Type: typ, Content: content, Sender: "claude-a", Tags: tags,
			EditedAt: time.Date(2026, 11, day, 9, 0, 0, 0, time.UTC)}
	}
	memories := snap(nil, []CanvasMemoryChannel{
		{Channel: "job1-memory-b", Memories: nil},
		{Channel: "job1-memory-a", Memories: []CanvasMemory{mem(3, strings.Repeat("long subject ", 12)[:150], "fact", "kept whole", nil, 2)}},
		{Channel: "job1-memory", Memories: []CanvasMemory{
			mem(1, "Postgres needs TCP keepalive", "fact", "Set it.\n# not a heading\n---\nlast\n#tail", []string{"area:db", "ops"}, 4),
			mem(2, "", "", strings.Repeat("0123456789", 10)+"\nsecond line", nil, 3),
		}},
	})

	esc := tsk(1, "a*b_c~d`e[f]g<h>i|j#k\r\nl", "in_progress", "a*b", 0)
	escaping := snap([]CanvasTaskList{{Channel: "job1_tasks", Tasks: []CanvasTask{esc}}}, []CanvasMemoryChannel{
		{Channel: "job1_mem", Memories: []CanvasMemory{mem(1, "#hash subject", "t_y", "body", []string{"a_b"}, 4)}},
	})

	big := strings.Repeat("m", 1500)
	cap := func(id int64, day int) CanvasMemory { return mem(id, "mem", "fact", big, nil, day) }
	sizeCap := snap(nil, []CanvasMemoryChannel{
		{Channel: "job1-mem-a", Memories: []CanvasMemory{cap(4, 4), cap(1, 1)}},
		{Channel: "job1-mem-b", Memories: []CanvasMemory{cap(3, 3), cap(2, 2)}},
	})

	var many []CanvasTask
	for i := int64(1); i <= 40; i++ {
		many = append(many, tsk(i, strings.Repeat("s", 200), "pending", "", 0))
	}
	tooLarge := snap([]CanvasTaskList{{Channel: "job1-tasks", Tasks: many}},
		[]CanvasMemoryChannel{{Channel: "job1-mem", Memories: []CanvasMemory{mem(1, "one", "fact", "x", nil, 4)}}})

	return map[string]CanvasSnapshot{
		"attention": attention,
		"attention-empty": snap([]CanvasTaskList{{Channel: "job1-tasks", Tasks: []CanvasTask{
			tsk(1, "One", "pending", "", 0), tsk(2, "Two", "pending", "", 0)}}}, nil),
		"tree":               tree,
		"completed-subtrees": completed,
		"parent-gone": snap([]CanvasTaskList{{Channel: "job1-tasks", Tasks: []CanvasTask{
			tsk(1, "A", "pending", "", 0), tsk(2, "B", "pending", "", 0), tsk(3, "Orphaned child", "pending", "", 0)}}}, nil),
		"empty-list":      snap([]CanvasTaskList{{Channel: "job1-tasks"}}, nil),
		"nothing-visible": snap(nil, nil),
		"memories":        memories,
		"escaping":        escaping,
		"size-cap":        sizeCap,
		"tasks-too-large": tooLarge,
	}
}

func TestRenderCanvasGolden(t *testing.T) {
	caps := map[string]int{"size-cap": 4_000, "tasks-too-large": 2_000}
	for name, s := range canvasCases() {
		t.Run(name, func(t *testing.T) {
			max := canvasMaxBytes
			if c, ok := caps[name]; ok {
				max = c
			}
			r := renderCanvas(s, max)
			if len(r.Markdown) > max {
				t.Fatalf("len = %d, over the cap %d", len(r.Markdown), max)
			}
			switch name {
			case "size-cap":
				if r.DroppedMemories != 2 || r.TasksTooLarge {
					t.Errorf("Dropped = %d, TasksTooLarge = %v; want 2, false", r.DroppedMemories, r.TasksTooLarge)
				}
				if n := strings.Count(r.Markdown, "_1 older memories not shown; search agentmesh for them._"); n != 2 {
					t.Errorf("trailers = %d, want 2", n)
				}
			case "tasks-too-large":
				if !r.TasksTooLarge {
					t.Errorf("TasksTooLarge = false")
				}
			default:
				if r.DroppedMemories != 0 || r.TasksTooLarge {
					t.Errorf("unexpected flags: %+v", r)
				}
			}
			path := filepath.Join("testdata", "canvas", name+".md")
			if *update {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(r.Markdown), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if r.Markdown != string(want) {
				t.Errorf("%s differs from the golden; run go test ./internal/slackbridge -run TestRenderCanvasGolden -update and read the diff\n--- got\n%s", path, r.Markdown)
			}
		})
	}
}

func TestRenderCanvasDefaultCap(t *testing.T) {
	s := snap(nil, []CanvasMemoryChannel{{Channel: "job1-mem", Memories: []CanvasMemory{
		{ID: 1, Subject: "huge", Content: strings.Repeat("x", 1_100_000), EditedAt: canvasAt}}}})
	r := RenderCanvas(s)
	if r.DroppedMemories != 1 || len(r.Markdown) > canvasMaxBytes || r.TasksTooLarge {
		t.Fatalf("Dropped = %d, len = %d, TasksTooLarge = %v", r.DroppedMemories, len(r.Markdown), r.TasksTooLarge)
	}
}

func TestCanvasGlyphBytes(t *testing.T) {
	md := RenderCanvas(snap(nil, nil)).Markdown
	for _, g := range []string{"⛔︎", "⚠︎", "◌", "▶", "✔", "⚑"} {
		if !strings.Contains(md, g) {
			t.Errorf("legend lacks %+q", g)
		}
	}
	if !strings.Contains(md, CanvasMarker) {
		t.Errorf("no marker in %q", md)
	}
}

func TestEscapeCanvas(t *testing.T) {
	for in, want := range map[string]string{
		"*": `\*`, "_": `\_`, "~": `\~`, "`": "\\`", "[": `\[`, "]": `\]`, "<": `\<`, ">": `\>`, "|": `\|`,
		"#x": `\#x`, "a#x": "a#x", "a\r\nb": "a  b", "a\nb": "a b", "plain": "plain", "": "",
	} {
		if got := escapeCanvas(in); got != want {
			t.Errorf("escapeCanvas(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCanvasHashIgnoresUpdated(t *testing.T) {
	s := canvasCases()["tree"]
	later := s
	later.At = s.At.Add(time.Hour)
	a, b := RenderCanvas(s), RenderCanvas(later)
	if a.Markdown == b.Markdown {
		t.Fatal("renderings should differ in the Updated line")
	}
	ha := CanvasHash(a.Markdown)
	if ha != CanvasHash(b.Markdown) {
		t.Errorf("hash changed with the update time")
	}
	if len(ha) != 64 || strings.Trim(ha, "0123456789abcdef") != "" {
		t.Errorf("hash %q is not 64 hex characters", ha)
	}
	changed := canvasCases()["tree"]
	changed.TaskLists = []CanvasTaskList{{Channel: "job1-tasks", Tasks: append([]CanvasTask(nil), changed.TaskLists[0].Tasks...)}}
	changed.TaskLists[0].Tasks[0].Subject = "different"
	if CanvasHash(RenderCanvas(changed).Markdown) == ha {
		t.Errorf("a changed subject hashed equal")
	}
	// A memory line that starts with "_Updated " is content, never stripped.
	m1 := snap(nil, []CanvasMemoryChannel{{Channel: "m", Memories: []CanvasMemory{{ID: 1, Subject: "s", Content: "_Updated x_"}}}})
	m2 := m1
	m2.MemoryChannels = []CanvasMemoryChannel{{Channel: "m", Memories: []CanvasMemory{{ID: 1, Subject: "s", Content: "_Updated y_"}}}}
	if CanvasHash(RenderCanvas(m1).Markdown) == CanvasHash(RenderCanvas(m2).Markdown) {
		t.Errorf("a memory line starting with _Updated was stripped from the hash")
	}
}

func TestRenderCanvasIsPure(t *testing.T) {
	for name, s := range canvasCases() {
		t.Run(name, func(t *testing.T) {
			before := canvasCases()[name]
			a, b := RenderCanvas(s), RenderCanvas(s)
			if a != b {
				t.Errorf("two renderings differ")
			}
			if !reflect.DeepEqual(s, before) {
				t.Errorf("rendering modified the snapshot")
			}
		})
	}
}
