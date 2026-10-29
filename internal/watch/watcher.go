package watch

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Options selects the sessions to watch.
type Options struct {
	Root  string        // project directory; sessions in it or below it match
	All   bool          // watch the sessions of all projects
	Since time.Duration // ignore sessions without activity in this period
	Tail  int64         // bytes of each log to read at the start, 0 for 8 MiB
}

// Snapshot is an immutable copy of all watched agents.
type Snapshot struct {
	Agents []Agent
	Notes  []string // problems with a source, for the message line
	// Elsewhere is the number of sessions with activity in the Since period
	// that belong to another project. It is 0 when the watcher watches all
	// projects.
	Elsewhere int
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
	// parked holds the sessions with no event in the Since period. The
	// watcher does not poll them. A scan moves a session back when its log
	// gets a write.
	parked   map[string]*session
	away     map[string]int // sessions of other projects, for each source
	sentAway int
	notes    map[string]string
	newNotes bool // the notes changed after the last snapshot
	reload   chan struct{}
	scope    chan bool
	lastScan time.Time
}

const (
	pollEvery = 400 * time.Millisecond
	scanEvery = 3 * time.Second
	walkEvery = 30 * time.Second // a full walk of the log folders
	snapEvent = 300
)

// New creates a watcher for the project in opt.Root.
func New(opt Options) *Watcher {
	opt.Root = filepath.Clean(opt.Root)
	w := &Watcher{opt: opt, reload: make(chan struct{}, 1), scope: make(chan bool, 1)}
	w.init()
	return w
}

// init drops all state. The sources start again with empty caches.
func (w *Watcher) init() {
	w.sessions = map[string]*session{}
	w.parked = map[string]*session{}
	w.away = map[string]int{}
	w.sentAway = 0
	w.notes, w.newNotes = map[string]string{}, false
	w.sources = []source{newClaude(), newCodex(), newGemini(), newAuggie(), newOpencode(), newOmp()}
}

// Once reads the sessions one time and returns the snapshot, for a caller
// that does not run the watcher.
func (w *Watcher) Once() Snapshot {
	defer w.closeSources()
	w.step(true)
	return w.snapshot()
}

// Reload drops all state and reads the sessions again.
func (w *Watcher) Reload() {
	select {
	case w.reload <- struct{}{}:
	default:
	}
}

// SetAll switches between the sessions of the project and the sessions of
// all projects, and reads the sessions again.
func (w *Watcher) SetAll(all bool) {
	select {
	case <-w.scope:
	default:
	}
	w.scope <- all
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
		case all := <-w.scope:
			w.opt.All = all
			w.closeSources()
			w.init()
			w.step(true)
			send(w.snapshot())
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
	now := time.Now()
	cutoff := now.Add(-w.opt.Since)
	changed := false
	if scan {
		w.lastScan = now
		for _, src := range w.sources {
			src.scan(w, cutoff)
		}
		for key, s := range w.parked {
			if s.mod.Before(cutoff) {
				// The log has no write in the Since period. A scan reads
				// it again from the start if it gets a write.
				delete(w.parked, key)
			}
		}
		changed = w.elsewhere() != w.sentAway
	}
	changed = changed || len(w.sessions) != n
	for key, s := range w.sessions {
		err := s.h.poll(s)
		switch {
		case err == nil:
			w.clearNote(key)
		case errors.Is(err, fs.ErrNotExist):
			// The log is gone, for example after the user archived the
			// session. A scan finds the session again if the log comes back.
			delete(w.sessions, key)
			w.clearNote(key)
			changed = true
			continue
		default:
			w.note(key, s.Source+": "+err.Error())
		}
		if w.stale(s, cutoff) {
			delete(w.sessions, key)
			w.clearNote(key)
			w.parked[key] = s
			changed = true
			continue
		}
		if s.dirty {
			changed = true
		}
	}
	return changed || w.newNotes
}

// stale reports whether the last event of a session is older than cutoff.
// For a log without events, it uses the time of the last write.
func (w *Watcher) stale(s *session, cutoff time.Time) bool {
	if w.opt.Since <= 0 {
		return false
	}
	last := s.Updated
	if last.IsZero() {
		last = s.mod
	}
	return !last.IsZero() && last.Before(cutoff)
}

func (w *Watcher) has(key string) bool { _, ok := w.sessions[key]; return ok }

// add starts to watch a session. mod is the time of the last write to its
// log that the scan saw.
func (w *Watcher) add(s *session, mod time.Time) {
	s.seen(mod)
	w.sessions[s.Key] = s
}

// parkedLog reports whether the session of key is parked. When its log has
// a write after mod of the session, it moves the session back to the
// watched sessions.
func (w *Watcher) parkedLog(key string, mod time.Time) bool {
	s, ok := w.parked[key]
	if !ok {
		return false
	}
	if mod.After(s.mod) {
		delete(w.parked, key)
		w.sessions[key] = s
	}
	return true
}

// note sets the problem of a session or a source.
func (w *Watcher) note(key, msg string) {
	if w.notes[key] != msg {
		w.notes[key] = msg
		w.newNotes = true
	}
}

func (w *Watcher) clearNote(key string) {
	if _, ok := w.notes[key]; ok {
		delete(w.notes, key)
		w.newNotes = true
	}
}

// inRoot reports whether dir is the project root or a directory below it.
// When the watcher watches all projects, each directory matches.
func (w *Watcher) inRoot(dir string) bool {
	if dir == "" {
		return false
	}
	if w.opt.All {
		return true
	}
	dir = filepath.Clean(dir)
	return dir == w.opt.Root || strings.HasPrefix(dir, w.opt.Root+string(filepath.Separator))
}

// elsewhere returns the number of sessions of other projects.
func (w *Watcher) elsewhere() int {
	if w.opt.All {
		return 0
	}
	n := 0
	for _, c := range w.away {
		n += c
	}
	return n
}

func (w *Watcher) snapshot() Snapshot {
	out := Snapshot{Agents: make([]Agent, 0, len(w.sessions)), Elsewhere: w.elsewhere()}
	w.sentAway, w.newNotes = out.Elsewhere, false
	for _, s := range w.sessions {
		s.dirty = false
		s.Wrote = s.mod
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

// logInfo is what a source knows about one log file from a scan.
type logInfo struct {
	cwd  string    // the working directory of the session, "" when unknown
	away bool      // the log belongs to another project
	mod  time.Time // the modification time at the last check
}

// logCache keeps the project of each log that a source checked, so that a
// scan reads the head of a log one time only. The watcher makes a new cache
// when the scope changes.
type logCache map[string]logInfo

// count returns the number of logs of other projects with a write at or
// after cutoff. When main is not nil, it counts only the logs that main
// accepts. It also removes the logs without such a write.
func (c logCache) count(cutoff time.Time, main func(string) bool) int {
	n := 0
	for f, info := range c {
		switch {
		case info.mod.Before(cutoff):
			delete(c, f)
		case info.away && (main == nil || main(f)):
			n++
		}
	}
	return n
}

// walkDue reports whether a source must walk all its log folders now, and
// records the time of the walk.
func walkDue(last *time.Time) bool {
	now := time.Now()
	if now.Sub(*last) < walkEvery {
		return false
	}
	*last = now
	return true
}
