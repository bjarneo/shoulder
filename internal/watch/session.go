package watch

import (
	"bytes"
	"io"
	"os"
	"time"
)

const (
	maxEvents   = 600
	keepEvents  = 400
	tailInitial = 8 << 20 // bytes of an existing log to read at startup
	maxLine     = 32 << 20
)

// session is the mutable state of one agent while the watcher reads its log.
type session struct {
	Agent
	calls map[string][]int
	h     handle
	dirty bool
}

// handle reads new data for one session.
type handle interface {
	poll(s *session) error
}

func newSession(key, source string, h handle) *session {
	return &session{Agent: Agent{Key: key, Source: source, Name: source, CtxMax: 200_000}, calls: map[string][]int{}, h: h}
}

// add appends events and returns the index of the first one.
func (s *session) add(t time.Time, evs ...Event) int {
	i := len(s.Events)
	for _, e := range evs {
		if e.Time.IsZero() {
			e.Time = t
		}
		if e.CallID != "" {
			s.calls[e.CallID] = append(s.calls[e.CallID], len(s.Events))
		}
		s.Events = append(s.Events, e)
	}
	s.touch(t)
	if len(s.Events) > maxEvents {
		s.trim()
		i = max(0, len(s.Events)-len(evs))
	}
	return i
}

func (s *session) touch(t time.Time) {
	if t.After(s.Updated) {
		s.Updated = t
	}
	if s.Started.IsZero() || (!t.IsZero() && t.Before(s.Started)) {
		s.Started = t
	}
	s.dirty = true
}

// call returns the indexes of the events of a tool call.
func (s *session) call(id string) []int { return s.calls[id] }

func (s *session) trim() {
	drop := len(s.Events) - keepEvents
	s.Events = append([]Event(nil), s.Events[drop:]...)
	s.calls = map[string][]int{}
	for i, e := range s.Events {
		if e.CallID != "" {
			s.calls[e.CallID] = append(s.calls[e.CallID], i)
		}
	}
}

// reset drops all events, for sources that rebuild the log on each change.
func (s *session) reset() {
	s.Events = s.Events[:0]
	s.calls = map[string][]int{}
	s.TurnEnded = false
	s.dirty = true
}

// prompt records a user prompt and starts a new turn.
func (s *session) prompt(t time.Time, text string) {
	text = oneLine(text, 400)
	if text == "" {
		return
	}
	if s.Prompt == "" {
		s.Prompt = oneLine(text, 120)
	}
	s.TurnEnded = false
	s.add(t, Event{Kind: KindYou, Text: text, Line: -1, EndLine: -1})
}

// say records agent text.
func (s *session) say(t time.Time, text string) {
	text = oneLine(text, 400)
	if text == "" {
		return
	}
	s.add(t, Event{Kind: KindSay, Text: text, Line: -1, EndLine: -1})
}

// endTurn records the end of a turn once.
func (s *session) endTurn(t time.Time, text string) {
	if s.TurnEnded {
		return
	}
	s.TurnEnded = true
	if text == "" {
		text = s.lastSay()
	}
	if text == "" {
		text = "turn ended"
	}
	s.add(t, Event{Kind: KindDone, Text: oneLine(text, 200), Line: -1, EndLine: -1})
}

// tail reads complete lines that are appended to a file.
type tail struct {
	path    string
	start   int64 // bytes to read at the first call, 0 for tailInitial
	off     int64
	partial []byte
	started bool
}

// lines calls fn for each new complete line. At the first call it skips all
// but the last t.start bytes of the file.
func (t *tail) lines(fn func([]byte)) error {
	f, err := os.Open(t.path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	size := st.Size()
	if size < t.off {
		// The file was truncated or replaced.
		t.off, t.partial, t.started = 0, nil, false
	}
	skipFirst := false
	if !t.started {
		t.started = true
		start := t.start
		if start <= 0 {
			start = tailInitial
		}
		if size > start {
			t.off = size - start
			skipFirst = true
		}
	}
	if size == t.off {
		return nil
	}
	if _, err := f.Seek(t.off, io.SeekStart); err != nil {
		return err
	}
	buf, err := io.ReadAll(io.LimitReader(f, size-t.off))
	if err != nil {
		return err
	}
	t.off += int64(len(buf))
	if skipFirst {
		i := bytes.IndexByte(buf, '\n')
		if i < 0 {
			return nil
		}
		buf = buf[i+1:]
	}
	if len(t.partial) > 0 {
		buf = append(t.partial, buf...)
		t.partial = nil
	}
	for len(buf) > 0 {
		i := bytes.IndexByte(buf, '\n')
		if i < 0 {
			if len(buf) < maxLine {
				t.partial = append([]byte(nil), buf...)
			}
			break
		}
		if line := bytes.TrimSpace(buf[:i]); len(line) > 0 {
			fn(line)
		}
		buf = buf[i+1:]
	}
	return nil
}

// headLine returns the first line of a file, read up to limit bytes.
func headLine(path string, limit int) []byte {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	buf := make([]byte, limit)
	n, _ := io.ReadFull(f, buf)
	buf = buf[:n]
	if i := bytes.IndexByte(buf, '\n'); i >= 0 {
		buf = buf[:i]
	}
	return buf
}

func home() string {
	h, _ := os.UserHomeDir()
	return h
}
