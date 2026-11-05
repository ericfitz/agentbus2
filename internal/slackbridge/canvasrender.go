package slackbridge

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// CanvasTitle is the canvas title (and tab label). CanvasMarker is the text
// the italic line under the h1 always contains: the bridge's marker.
const (
	CanvasTitle  = "Status"
	CanvasMarker = "changes go through agentmesh"

	// canvasMaxBytes is 1 MiB minus a 4 KiB margin (spec 4.6).
	canvasMaxBytes = 1_044_480
)

const (
	glyphPending   = "◌"  // ◌
	glyphProgress  = "▶"  // ▶
	glyphDone      = "✔"  // ✔
	glyphBlocked   = "⛔︎" // ⛔︎
	glyphProblem   = "⚠︎" // ⚠︎
	glyphHuman     = "⚑"  // ⚑
	canvasSep      = " · "
	canvasNoTasks  = "_No tasks._"
	canvasNoMems   = "_No memories._"
	canvasNothing  = "_Nothing needs attention._"
	canvasNoneSeen = "_Nothing in this scope is visible to every member of this channel._"
	canvasTooLarge = "_The task lists are too large for a canvas._"
	statusMaxRunes = 120
	headMaxRunes   = 80
)

var canvasLegend = strings.Join([]string{
	glyphPending + " pending", glyphProgress + " in progress", glyphDone + " done",
	glyphBlocked + " blocked", glyphProblem + " stalled or problem", glyphHuman + " needs human",
}, canvasSep)

// CanvasRendering is the markdown of one snapshot. DroppedMemories counts the
// memories the size cap left out; TasksTooLarge is set when the task lists
// alone exceed the cap, and the body then holds only the header lines.
type CanvasRendering struct {
	Markdown        string
	DroppedMemories int
	TasksTooLarge   bool
}

// RenderCanvas renders s under the canvas size cap. It is pure.
func RenderCanvas(s CanvasSnapshot) CanvasRendering { return renderCanvas(s, canvasMaxBytes) }

// memRef names one memory of the channel-sorted snapshot.
type memRef struct{ ch, idx int }

func renderCanvas(s CanvasSnapshot, maxBytes int) CanvasRendering {
	lists := append([]CanvasTaskList(nil), s.TaskLists...)
	sort.SliceStable(lists, func(i, j int) bool { return lists[i].Channel < lists[j].Channel })
	mems := append([]CanvasMemoryChannel(nil), s.MemoryChannels...)
	sort.SliceStable(mems, func(i, j int) bool { return mems[i].Channel < mems[j].Channel })

	var refs []memRef
	for ci, mc := range mems {
		for mi := range mc.Memories {
			refs = append(refs, memRef{ci, mi})
		}
	}
	// Oldest edit first, across all channels.
	sort.SliceStable(refs, func(i, j int) bool {
		a, b := mems[refs[i].ch].Memories[refs[i].idx], mems[refs[j].ch].Memories[refs[j].idx]
		if !a.EditedAt.Equal(b.EditedAt) {
			return a.EditedAt.Before(b.EditedAt)
		}
		return a.ID < b.ID
	})
	build := func(k int) string {
		dropped := make(map[memRef]bool, k)
		for _, r := range refs[:k] {
			dropped[r] = true
		}
		return buildCanvas(s, lists, mems, dropped)
	}

	if full := build(0); len(full) <= maxBytes {
		return CanvasRendering{Markdown: full}
	}
	total := len(refs)
	if all := build(total); len(all) > maxBytes {
		return CanvasRendering{Markdown: tooLargeCanvas(s), DroppedMemories: total, TasksTooLarge: true}
	}
	// Smallest k that fits. Dropping a memory can add a trailer, so the size
	// is not strictly monotone in k: search, then step up until it fits.
	lo, hi := 1, total
	for lo < hi {
		mid := (lo + hi) / 2
		if len(build(mid)) <= maxBytes {
			hi = mid
		} else {
			lo = mid + 1
		}
	}
	md := build(lo)
	for len(md) > maxBytes {
		lo++
		md = build(lo)
	}
	return CanvasRendering{Markdown: md, DroppedMemories: lo}
}

func canvasHeader(s CanvasSnapshot) []string {
	return []string{
		"# " + escapeCanvas(s.ScopeName) + " · tasks and memory\n" +
			"_Updated " + s.At.UTC().Format("2006-01-02 15:04") + " UTC · read-only; " + CanvasMarker + "_",
		canvasLegend,
	}
}

func tooLargeCanvas(s CanvasSnapshot) string {
	return strings.Join(append(canvasHeader(s), canvasTooLarge), "\n\n") + "\n"
}

func buildCanvas(s CanvasSnapshot, lists []CanvasTaskList, mems []CanvasMemoryChannel, dropped map[memRef]bool) string {
	blocks := canvasHeader(s)
	if len(lists) == 0 && len(mems) == 0 {
		blocks = append(blocks, canvasNoneSeen)
		return strings.Join(blocks, "\n\n") + "\n"
	}
	var groups [][]string
	groups = append(groups, attentionBlocks(lists))
	if len(lists) > 0 {
		var g []string
		for _, l := range lists {
			g = append(g, taskListBlocks(l)...)
		}
		groups = append(groups, g)
	}
	if len(mems) > 0 {
		var g []string
		for ci, mc := range mems {
			g = append(g, memoryBlocks(ci, mc, dropped)...)
		}
		groups = append(groups, g)
	}
	for i, g := range groups {
		if i > 0 {
			blocks = append(blocks, "---")
		}
		blocks = append(blocks, g...)
	}
	return strings.Join(blocks, "\n\n") + "\n"
}

// taskView is the derived display of one task: its glyph, problem suffix and
// which attention subsections it belongs to.
type taskView struct {
	glyph, suffix string
	done          bool
	blocked       bool
	human         bool
	stalled       bool
	problem       bool
}

func viewOf(t CanvasTask) taskView {
	if t.Status == "completed" {
		return taskView{glyph: glyphDone, done: true}
	}
	v := taskView{
		blocked: len(t.OpenBlockers) > 0,
		human:   t.OwnerStatus != nil && t.OwnerStatus.Tag == "needs-human",
		stalled: t.Stalled,
		problem: t.OwnerStatus != nil && t.OwnerStatus.Problem != "",
	}
	switch {
	case v.blocked:
		v.glyph, v.suffix = glyphBlocked, "blocked by "+blockerList(t.OpenBlockers)
	case v.human:
		v.glyph, v.suffix = glyphHuman, "needs human"
		if txt := t.OwnerStatus.Text; txt != "" {
			v.suffix += ": " + cutEscape(txt, statusMaxRunes)
		}
	case v.stalled:
		v.glyph, v.suffix = glyphProblem, "stalled: "+stalledText(t)
	case v.problem:
		v.glyph, v.suffix = glyphProblem, cutEscape(t.OwnerStatus.Problem, statusMaxRunes)
	case t.Status == "in_progress":
		v.glyph = glyphProgress
	default:
		v.glyph = glyphPending
	}
	return v
}

func blockerList(ids []int64) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = "#" + strconv.FormatInt(id, 10)
	}
	return strings.Join(parts, ", ")
}

func stalledText(t CanvasTask) string {
	if !t.LeaseExpiredAt.IsZero() {
		return "lease expired " + t.LeaseExpiredAt.UTC().Format("15:04") + " UTC"
	}
	return "owner offline"
}

func taskLine(t CanvasTask, v taskView) string {
	line := v.glyph + " #" + strconv.FormatInt(t.ID, 10) + " " + escapeCanvas(t.Subject)
	if t.Assignee != "" {
		line += canvasSep + escapeCanvas(t.Assignee)
	}
	if v.suffix != "" {
		line += canvasSep + v.suffix
	}
	return line
}

func taskListBlocks(l CanvasTaskList) []string {
	head := "## Tasks: " + escapeCanvas(l.Channel)
	if len(l.Tasks) == 0 {
		return []string{head + "\n" + canvasNoTasks}
	}
	counts := map[string]int{}
	for _, t := range l.Tasks {
		counts[viewOf(t).glyph]++
	}
	var parts []string
	for _, g := range []string{glyphProgress, glyphPending, glyphBlocked, glyphProblem, glyphHuman, glyphDone} {
		if n := counts[g]; n > 0 {
			parts = append(parts, g+" "+strconv.Itoa(n))
		}
	}
	out := []string{head + "\n" + strings.Join(parts, canvasSep)}
	var cur []string
	flush := func() {
		if len(cur) > 0 {
			out = append(out, strings.Join(cur, "\n"))
			cur = nil
		}
	}
	for _, t := range l.Tasks {
		v := viewOf(t)
		if t.Depth <= 0 {
			flush()
			cur = append(cur, "### "+taskLine(t, v))
			continue
		}
		box := "[ ]"
		if v.done {
			box = "[x]"
		}
		cur = append(cur, strings.Repeat("  ", t.Depth-1)+"- "+box+" "+taskLine(t, v))
	}
	flush()
	return out
}

func attentionBlocks(lists []CanvasTaskList) []string {
	type sub struct {
		title string
		lines []string
	}
	subs := []*sub{{title: glyphBlocked + " Blocked"}, {title: glyphHuman + " Needs human"},
		{title: glyphProblem + " Stalled"}, {title: glyphProblem + " Problem reported"}}
	for _, l := range lists {
		ch := escapeCanvas(l.Channel)
		for _, t := range l.Tasks {
			v := viewOf(t)
			if v.done {
				continue
			}
			base := "#" + strconv.FormatInt(t.ID, 10) + " " + escapeCanvas(t.Subject)
			owner := ""
			if t.Assignee != "" {
				owner = canvasSep + escapeCanvas(t.Assignee)
			}
			if v.blocked {
				subs[0].lines = append(subs[0].lines, "- "+glyphBlocked+" "+base+canvasSep+"blocked by "+blockerList(t.OpenBlockers)+canvasSep+ch)
			}
			if v.human {
				txt := ""
				if t.OwnerStatus.Text != "" {
					txt = canvasSep + cutEscape(t.OwnerStatus.Text, statusMaxRunes)
				}
				subs[1].lines = append(subs[1].lines, "- "+glyphHuman+" "+base+owner+txt+canvasSep+ch)
			}
			if v.stalled {
				subs[2].lines = append(subs[2].lines, "- "+glyphProblem+" "+base+owner+canvasSep+stalledText(t)+canvasSep+ch)
			}
			if v.problem {
				subs[3].lines = append(subs[3].lines, "- "+glyphProblem+" "+base+owner+canvasSep+cutEscape(t.OwnerStatus.Problem, statusMaxRunes)+canvasSep+ch)
			}
		}
	}
	out := []string{"## Needs attention"}
	any := false
	for _, sb := range subs {
		if len(sb.lines) > 0 {
			any = true
			out = append(out, "### "+sb.title+"\n"+strings.Join(sb.lines, "\n"))
		}
	}
	if !any {
		out = append(out, canvasNothing)
	}
	return out
}

func memoryBlocks(ci int, mc CanvasMemoryChannel, dropped map[memRef]bool) []string {
	out := []string{"## Memory: " + escapeCanvas(mc.Channel)}
	n := 0
	for mi, m := range mc.Memories {
		if dropped[memRef{ci, mi}] {
			n++
			continue
		}
		out = append(out, memoryBlock(m))
	}
	if len(mc.Memories) == 0 {
		out = append(out, canvasNoMems)
	}
	if n > 0 {
		out = append(out, fmt.Sprintf("_%d older memories not shown; search agentmesh for them._", n))
	}
	return out
}

func memoryBlock(m CanvasMemory) string {
	head := escapeCanvas(m.Subject)
	if m.Subject == "" {
		first, _, _ := strings.Cut(m.Content, "\n")
		head = cutEscape(strings.TrimRight(first, "\r"), headMaxRunes)
	}
	if strings.TrimSpace(head) == "" {
		head = "Memory \\#" + strconv.FormatInt(m.ID, 10)
	}
	var meta []string
	if m.Type != "" {
		meta = append(meta, escapeCanvas(m.Type))
	}
	for _, tg := range m.Tags {
		meta = append(meta, escapeCanvas(tg))
	}
	var edited []string
	if !m.EditedAt.IsZero() {
		edited = append(edited, "edited "+m.EditedAt.UTC().Format("2006-01-02"))
	}
	if m.Sender != "" {
		edited = append(edited, "by "+escapeCanvas(m.Sender))
	}
	if len(edited) > 0 {
		meta = append(meta, strings.Join(edited, " "))
	}
	b := "### " + head
	if len(meta) > 0 {
		b += "\n_" + strings.Join(meta, canvasSep) + "_"
	}
	if c := strings.TrimRight(m.Content, "\n"); c != "" {
		lines := strings.Split(c, "\n")
		for i, ln := range lines {
			if strings.HasPrefix(ln, "#") {
				lines[i] = "\\" + ln
			}
		}
		b += "\n\n" + strings.Join(lines, "\n")
	}
	return b
}

// cutEscape cuts s to max characters plus an ellipsis, then escapes it.
func cutEscape(s string, max int) string {
	r := []rune(s)
	if len(r) > max {
		return escapeCanvas(string(r[:max])) + "…"
	}
	return escapeCanvas(s)
}

// escapeCanvas makes s render literally on one canvas line.
func escapeCanvas(s string) string {
	var b strings.Builder
	for i, r := range s {
		switch r {
		case '\n', '\r':
			b.WriteByte(' ')
		case '*', '_', '~', '`', '[', ']', '<', '>', '|':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '#':
			if i == 0 {
				b.WriteByte('\\')
			}
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// CanvasHash is the SHA-256 (hex) of markdown without its second line, the
// "Updated" line whose position the renderer fixes.
func CanvasHash(markdown string) string {
	body := markdown
	if i := strings.IndexByte(markdown, '\n'); i >= 0 {
		rest := markdown[i+1:]
		if j := strings.IndexByte(rest, '\n'); j >= 0 {
			body = markdown[:i+1] + rest[j+1:]
		}
	}
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}
