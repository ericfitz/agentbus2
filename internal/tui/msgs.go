package tui

import "github.com/ericfitz/agentbus2/internal/bus"

// Messages delivered to Model.Update (ported from v1 internal/tui/msgs.go).
// Every bus call the model makes runs in a tea.Cmd and comes back as one of
// these; the receive goroutine sends batchMsg, receiveErrMsg and
// sessionLostMsg directly. They live in one file so feature files never add
// message types.
type (
	// batchMsg is one delivery batch from the receive loop.
	batchMsg struct{ res bus.ReceiveResult }
	// receiveErrMsg reports one failed call of the receive loop.
	receiveErrMsg struct{ err error }
	// sessionLostMsg ends the program: the session was replaced or closed.
	sessionLostMsg struct{ err error }
	// statusTickMsg is the periodic status timer.
	statusTickMsg struct{}
	// statusMsg is the rail's source: channels, identities, tag
	// subscriptions and Status (answered or not, as the server decides).
	statusMsg struct {
		channels   []bus.Channel
		identities []bus.IdentityInfo
		tags       [][]string
		admin      bool
		keepAdmin  bool  // Status failed for another reason than forbidden
		statusFail error // that failure, shown in the status bar
		status     bus.Status
		statusOK   bool
		err        error
	}
	// usageMsg carries the storage usage (a *bus.Usage once Milestone 7
	// Task 6 is on main), sent from the health overlay only.
	usageMsg struct {
		usage any
		err   error
	}
	// healthMsg carries the server's tiered health document, requested when
	// the health overlay opens.
	healthMsg struct {
		h   *bus.Health
		err error
	}
	// historyMsg is a window of channel history.
	historyMsg struct {
		channel string
		msgs    []bus.Message
		prepend bool
		err     error
	}
	// sentMsg reports a Send.
	sentMsg struct {
		channel string
		err     error
	}
	// searchMsg is a search result; query is the query that produced res, so
	// a stale response is ignored.
	searchMsg struct {
		query string
		res   bus.SearchResult
		err   error
	}
	// revisionsMsg is the revision list of the live row seq.
	revisionsMsg struct {
		seq  int64
		revs []bus.Message
		err  error
	}
	// configEditedMsg reports the config editor's return.
	configEditedMsg struct{ err error }
	// toastClearMsg clears the toast numbered seq.
	toastClearMsg struct{ seq int }
	// subscribedMsg reports a Subscribe.
	subscribedMsg struct {
		channel string
		err     error
	}
	// tasksMsg is a task list.
	tasksMsg struct {
		ch    string
		tasks []bus.TaskSummary
		err   error
	}
	// taskMsg is one task.
	taskMsg struct {
		task bus.Task
		err  error
	}
)
