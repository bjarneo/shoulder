package watch

import (
	"context"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Options selects the sessions to watch.
type Options struct {
	Root  string        // project directory; sessions in it or below it match
	Since time.Duration // ignore sessions without activity in this period
}

// Snapshot is an immutable copy of all watched agents.
type Snapshot struct {
	Agents []Agent
	Notes  []string // problems with a source, for the message line
}

// source finds sessions of one agent type.
type source interface {
	scan(w *Watcher, cutoff time.Time)
	close()
}

// Watcher polls the session logs of all supported agents.
type Watcher struct {
	opt      Options
	sources  []source
	sessions map[string]*session
	notes    map[string]string
	reload   chan struct{}
	lastScan time.Time
}

const (
	pollEvery = 400 * time.Millisecond
	scanEvery = 3 * time.Second
	snapEvent = 300
)

// New creates a watcher for the project in opt.Root.
func New(opt Options) *Watcher {
	opt.Root = filepath.Clean(opt.Root)
	w := &Watcher{opt: opt, reload: make(chan struct{}, 1)}
	w.init()
	return w
}

func (w *Watcher) init() {
	w.sessions = map[string]*session{}
	w.notes = map[string]string{}
	w.sources = []source{newClaude(), newCodex(), newGemini(), newOpencode(), newOmp()}
}

// Reload drops all state and reads the sessions again.
func (w *Watcher) Reload() {
	select {
	case w.reload <- struct{}{}:
	default:
	}
}

// Run polls until ctx ends. It calls send with a new snapshot after each
// change.
func (w *Watcher) Run(ctx context.Context, send func(Snapshot)) {
	t := time.NewTicker(pollEvery)
	defer t.Stop()
	defer w.closeSources()
	w.step(true)
	send(w.snapshot())
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.reload:
			w.closeSources()
			w.init()
			w.step(true)
			send(w.snapshot())
		case <-t.C:
			if w.step(time.Since(w.lastScan) > scanEvery) {
				send(w.snapshot())
			}
		}
	}
}

func (w *Watcher) closeSources() {
	for _, s := range w.sources {
		s.close()
	}
}

// step scans for new sessions when scan is true, then polls all sessions.
// It reports whether anything changed.
func (w *Watcher) step(scan bool) bool {
	n := len(w.sessions)
	if scan {
		w.lastScan = time.Now()
		cutoff := w.lastScan.Add(-w.opt.Since)
		for _, src := range w.sources {
			src.scan(w, cutoff)
		}
	}
	changed := len(w.sessions) != n
	for key, s := range w.sessions {
		if err := s.h.poll(s); err != nil {
			w.note(key, s.Source+": "+err.Error())
		}
		if s.dirty {
			changed = true
		}
	}
	return changed
}

func (w *Watcher) has(key string) bool { _, ok := w.sessions[key]; return ok }

func (w *Watcher) add(s *session) { w.sessions[s.Key] = s }

func (w *Watcher) note(key, msg string) { w.notes[key] = msg }

// inRoot reports whether dir is the project root or a directory below it.
func (w *Watcher) inRoot(dir string) bool {
	if dir == "" {
		return false
	}
	dir = filepath.Clean(dir)
	return dir == w.opt.Root || strings.HasPrefix(dir, w.opt.Root+string(filepath.Separator))
}

func (w *Watcher) snapshot() Snapshot {
	out := Snapshot{Agents: make([]Agent, 0, len(w.sessions))}
	for _, s := range w.sessions {
		s.dirty = false
		// Sessions with no events yet, such as a fresh log, stay hidden.
		if len(s.Events) == 0 {
			continue
		}
		a := s.Agent
		ev := s.Events
		if len(ev) > snapEvent {
			ev = ev[len(ev)-snapEvent:]
		}
		a.Events = append([]Event(nil), ev...)
		out.Agents = append(out.Agents, a)
	}
	sort.Slice(out.Agents, func(i, j int) bool {
		a, b := out.Agents[i], out.Agents[j]
		if !a.Started.Equal(b.Started) {
			return a.Started.Before(b.Started)
		}
		return a.Key < b.Key
	})
	for _, n := range w.notes {
		out.Notes = append(out.Notes, n)
	}
	sort.Strings(out.Notes)
	return out
}
