// Package watch reads the session logs of coding agents and turns them into
// one event model. It supports Claude Code, Codex, Gemini CLI, Auggie, opencode
// and omp.
package watch

import (
	"strings"
	"time"
)

// Kind is the type of an activity event.
type Kind string

const (
	KindYou  Kind = "you"  // a prompt from the user
	KindSay  Kind = "say"  // text from the agent
	KindRead Kind = "read" // a file read
	KindGrep Kind = "grep" // a content or file name search
	KindList Kind = "list" // a directory listing
	KindEdit Kind = "edit" // a file write, edit or patch
	KindRun  Kind = "run"  // a shell command
	KindWeb  Kind = "web"  // a web fetch or search
	KindTask Kind = "task" // a subagent spawn
	KindPlan Kind = "plan" // a todo or plan update
	KindAsk  Kind = "ask"  // a question to the user
	KindTool Kind = "tool" // any other tool
	KindDone Kind = "done" // the end of a turn
)

// Result is the outcome of a tool call.
type Result uint8

const (
	Pending Result = iota
	OK
	Failed
	Warned
)

// Event is one entry in the activity log of an agent.
type Event struct {
	Time   time.Time
	Kind   Kind
	Tool   string
	CallID string

	File    string // absolute path
	Line    int    // first line, 0-based, -1 when unknown
	EndLine int    // last line, 0-based, -1 when unknown
	Needle  string // text that an edit inserted, to find the edit in the file

	Query    string
	Hits     int // -1 when unknown
	Cmd      string
	Output   []string
	Result   Result
	Exit     int
	Approval bool // the agent waits for the user to approve this call

	Text    string
	Add     int
	Del     int
	Created bool
	Deleted bool
}

// Agent is one agent session.
type Agent struct {
	Key    string // stable identity, unique across sources
	Source string // claude, codex, gemini, auggie, opencode or omp
	Name   string // short label, such as "claude" or a subagent type
	Title  string // session title, when the agent writes one
	Prompt string // first prompt in the visible part of the log
	Parent string // key of the parent session for a subagent
	Cwd    string

	Started time.Time
	Updated time.Time
	Wrote   time.Time // last write to the session log, also by lines without events
	Events  []Event

	TurnEnded bool
	Ctx       int // tokens in the context window
	CtxMax    int
}

// Task is the one-line description of the work of the agent.
func (a *Agent) Task() string {
	if a.Title != "" {
		return a.Title
	}
	if a.Prompt != "" {
		return a.Prompt
	}
	return "no prompt yet"
}

// Status is the coarse state of an agent.
type Status uint8

const (
	Active Status = iota
	Waiting
	Done
)

// Activity is what an agent does at a given time.
type Activity struct {
	Status Status
	Act    string // thinking, reading, editing, running, searching, waiting, done, idle
	Detail string
	// Cur is the index of the event that sets the file position, or -1.
	Cur int
}

const (
	approvalAfter = 20 * time.Second
	idleAfter     = 10 * time.Minute
	lostAfter     = 30 * time.Minute // a pending call without a log write for this long has no result
	lingerFor     = 2 * time.Second
)

// Activity derives the current state of the agent from its events.
func (a *Agent) Activity(now time.Time) Activity {
	act := Activity{Cur: a.position()}
	if a.TurnEnded {
		act.Status, act.Act = Done, "done"
		act.Detail = a.lastSay()
		if act.Detail == "" {
			act.Detail = "turn ended"
		}
		return act
	}
	if i := a.pending(); i >= 0 {
		e := &a.Events[i]
		age := now.Sub(e.Time)
		switch {
		case e.Approval:
			act.Status, act.Act, act.Detail = Waiting, "waiting", withAge("on you · approve "+e.Tool, age)
		case e.Kind == KindAsk:
			act.Status, act.Act, act.Detail = Waiting, "waiting", withAge("on you · "+firstLine(e.Text), age)
		case e.Kind == KindRun || e.Kind == KindTask || e.Kind == KindWeb:
			if now.Sub(a.lastWrite()) >= lostAfter {
				// The agent stopped, for example when the user closed its
				// terminal, and the call never got a result.
				act.Status, act.Act, act.Detail = Done, "idle", "no result · "+detailOf(e)
				return act
			}
			act.Status, act.Act, act.Detail = Active, actOf(e.Kind), withAge(detailOf(e), age)
		case age > approvalAfter:
			act.Status, act.Act, act.Detail = Waiting, "waiting", withAge("on you · approve "+e.Tool, age)
		default:
			act.Status, act.Act, act.Detail = Active, actOf(e.Kind), detailOf(e)
		}
		return act
	}
	if now.Sub(a.Updated) > idleAfter {
		act.Status, act.Act = Done, "idle"
		act.Detail = "quiet for " + Ago(now.Sub(a.Updated))
		return act
	}
	act.Status, act.Act = Active, "thinking"
	if n := len(a.Events); n > 0 {
		e := &a.Events[n-1]
		if isTool(e.Kind) && now.Sub(e.Time) < lingerFor {
			act.Act, act.Detail = actOf(e.Kind), detailOf(e)
			return act
		}
	}
	if act.Cur >= 0 {
		act.Detail = shortPath(a.Events[act.Cur].File, a.Cwd)
	} else {
		act.Detail = "…"
	}
	return act
}

// lastWrite returns the time of the last write to the session log.
func (a *Agent) lastWrite() time.Time {
	if a.Wrote.After(a.Updated) {
		return a.Wrote
	}
	return a.Updated
}

// withAge puts the age of a call before its detail, when the call is one
// minute old or older.
func withAge(detail string, age time.Duration) string {
	if age < time.Minute {
		return detail
	}
	return Ago(age) + " · " + detail
}

// pending returns the index of the last tool call without a result in the
// current turn, or -1.
func (a *Agent) pending() int {
	for i := len(a.Events) - 1; i >= 0; i-- {
		e := &a.Events[i]
		if e.Kind == KindYou || e.Kind == KindDone {
			return -1
		}
		if isTool(e.Kind) && e.Result == Pending {
			return i
		}
	}
	return -1
}

// position returns the index of the last event that has a file and a line.
func (a *Agent) position() int {
	for i := len(a.Events) - 1; i >= 0; i-- {
		e := &a.Events[i]
		if e.File != "" && (e.Kind == KindRead || e.Kind == KindEdit) && !e.Deleted {
			return i
		}
	}
	return -1
}

func (a *Agent) lastSay() string {
	for i := len(a.Events) - 1; i >= 0; i-- {
		switch a.Events[i].Kind {
		case KindSay:
			return firstLine(a.Events[i].Text)
		case KindYou:
			return ""
		}
	}
	return ""
}

func isTool(k Kind) bool {
	switch k {
	case KindYou, KindSay, KindDone:
		return false
	}
	return true
}

func actOf(k Kind) string {
	switch k {
	case KindRead:
		return "reading"
	case KindGrep, KindList:
		return "searching"
	case KindEdit:
		return "editing"
	case KindRun:
		return "running"
	case KindWeb:
		return "browsing"
	case KindTask:
		return "delegating"
	case KindPlan:
		return "planning"
	case KindAsk:
		return "waiting"
	}
	return "working"
}

func detailOf(e *Event) string {
	switch e.Kind {
	case KindRead, KindEdit:
		return shortPath(e.File, "")
	case KindRun:
		return "$ " + firstLine(e.Cmd)
	case KindGrep:
		return "grep \"" + e.Query + "\""
	}
	return firstLine(e.Text)
}

// Ago formats a duration as a short age, such as "12m" or "3h".
func Ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return itoa(int(d.Seconds())) + "s"
	case d < time.Hour:
		return itoa(int(d.Minutes())) + "m"
	}
	return itoa(int(d.Hours())) + "h"
}

func shortPath(p, cwd string) string {
	if cwd != "" && strings.HasPrefix(p, cwd+"/") {
		return p[len(cwd)+1:]
	}
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	return s
}
