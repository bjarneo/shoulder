package watch

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// opencode 2.x keeps sessions in a SQLite database: session_v2 holds one row
// per session and session_message holds the messages with their tool calls.
type opencode struct {
	path   string
	db     *sql.DB
	broken bool
}

func newOpencode() *opencode {
	dir := os.Getenv("XDG_DATA_HOME")
	if dir == "" {
		dir = filepath.Join(home(), ".local", "share")
	}
	return &opencode{path: filepath.Join(dir, "opencode", "opencode.db")}
}

func (o *opencode) close() {
	if o.db != nil {
		o.db.Close()
		o.db = nil
	}
}

func (o *opencode) open() error {
	if o.db != nil {
		return nil
	}
	if _, err := os.Stat(o.path); err != nil {
		return err
	}
	db, err := sql.Open("sqlite", "file:"+o.path+"?mode=ro&_pragma=busy_timeout(3000)")
	if err != nil {
		return err
	}
	db.SetMaxOpenConns(1)
	var n int
	if err := db.QueryRow(`select count(*) from sqlite_master where name = 'session_v2'`).Scan(&n); err != nil {
		db.Close()
		return err
	}
	if n == 0 {
		db.Close()
		return errors.New("the database has no session_v2 table; shoulder reads opencode 2.x")
	}
	o.db = db
	return nil
}

func (o *opencode) scan(w *Watcher, cutoff time.Time) {
	if o.broken {
		return
	}
	if err := o.open(); err != nil {
		if !os.IsNotExist(err) {
			w.note("opencode", "opencode: "+err.Error())
			o.broken = true
		}
		return
	}
	rows, err := o.db.Query(`select id, directory, coalesce(parent_id, ''), coalesce(agent, ''), time_updated
		from session_v2 where time_updated >= ? and time_archived is null`, cutoff.UnixMilli())
	if err != nil {
		w.note("opencode", "opencode: "+err.Error())
		return
	}
	defer rows.Close()
	w.clearNote("opencode")
	away := 0
	for rows.Next() {
		var id, dir, parent, agent string
		var updated int64
		if rows.Scan(&id, &dir, &parent, &agent, &updated) != nil {
			continue
		}
		if !w.inRoot(dir) {
			if dir != "" && parent == "" {
				away++
			}
			continue
		}
		key := "opencode:" + id
		mod := time.UnixMilli(updated)
		if w.has(key) || w.parkedLog(key, mod) {
			continue
		}
		s := newSession(key, "opencode", &opencodeHandle{o: o, id: id, msgs: map[string]*ocMsg{}})
		s.Cwd = dir
		if parent != "" {
			s.Parent = "opencode:" + parent
			if agent != "" {
				s.Name = agent
			}
		}
		w.add(s, mod)
	}
	w.away["opencode"] = away
}

type opencodeHandle struct {
	o       *opencode
	id      string
	msgs    map[string]*ocMsg
	lastSeq int64
	started bool
	meta    time.Time
}

type ocMsg struct {
	seq     int64
	typ     string
	created int64
	updated int64
	data    string
}

const ocWindow = 300

func (h *opencodeHandle) poll(s *session) error {
	if h.o.db == nil {
		return nil
	}
	var rows *sql.Rows
	var err error
	if !h.started {
		rows, err = h.o.db.Query(`select id, type, seq, time_created, time_updated, data from session_message
			where session_id = ? order by seq desc limit ?`, h.id, ocWindow)
	} else {
		// Only the last messages change while the agent streams.
		rows, err = h.o.db.Query(`select id, type, seq, time_created, time_updated, data from session_message
			where session_id = ? and seq >= ? order by seq`, h.id, h.lastSeq-20)
	}
	if err != nil {
		return err
	}
	changed := false
	for rows.Next() {
		var id string
		var m ocMsg
		if rows.Scan(&id, &m.typ, &m.seq, &m.created, &m.updated, &m.data) != nil {
			continue
		}
		s.seen(time.UnixMilli(m.updated))
		if old, ok := h.msgs[id]; ok && old.updated == m.updated && len(old.data) == len(m.data) {
			continue
		}
		h.msgs[id] = &m
		h.lastSeq = max(h.lastSeq, m.seq)
		changed = true
	}
	rows.Close()
	h.started = true
	if time.Since(h.meta) > scanEvery {
		h.meta = time.Now()
		var title string
		var updated int64
		if h.o.db.QueryRow(`select coalesce(title, ''), time_updated from session_v2 where id = ?`, h.id).Scan(&title, &updated) == nil {
			// The scan compares this time with the time of a parked session.
			s.seen(time.UnixMilli(updated))
			if title != "" && !strings.HasPrefix(title, "New session") && title != s.Title {
				s.Title = title
				s.dirty = true
			}
		}
	}
	if changed {
		h.rebuild(s)
	}
	return nil
}

type ocData struct {
	Text    string `json:"text"`
	Outcome string `json:"outcome"`
	Content []struct {
		Type  string `json:"type"`
		Text  string `json:"text"`
		ID    string `json:"id"`
		Name  string `json:"name"`
		State struct {
			Status   string          `json:"status"`
			Input    map[string]any  `json:"input"`
			Content  json.RawMessage `json:"content"`
			Output   string          `json:"output"`
			Metadata map[string]any  `json:"metadata"`
			Time     struct {
				Created int64 `json:"created"`
			} `json:"time"`
		} `json:"state"`
	} `json:"content"`
	Tokens *struct {
		Input int `json:"input"`
		Cache struct {
			Read  int `json:"read"`
			Write int `json:"write"`
		} `json:"cache"`
	} `json:"tokens"`
}

func (h *opencodeHandle) rebuild(s *session) {
	list := make([]*ocMsg, 0, len(h.msgs))
	for _, m := range h.msgs {
		list = append(list, m)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].seq < list[j].seq })
	if len(list) > ocWindow {
		for _, m := range list[:len(list)-ocWindow] {
			for id, x := range h.msgs {
				if x == m {
					delete(h.msgs, id)
				}
			}
		}
		list = list[len(list)-ocWindow:]
	}
	s.reset()
	for _, m := range list {
		var d ocData
		if json.Unmarshal([]byte(m.data), &d) != nil {
			continue
		}
		t := time.UnixMilli(m.created)
		switch m.typ {
		case "user":
			s.prompt(t, d.Text)
		case "assistant":
			for _, c := range d.Content {
				switch c.Type {
				case "text":
					s.say(t, c.Text)
				case "tool":
					tt := t
					if c.State.Time.Created > 0 {
						tt = time.UnixMilli(c.State.Time.Created)
					}
					evs := toolEvents(c.Name, args(c.State.Input), s.Cwd, toolStyle{})
					out := c.State.Output
					if out == "" {
						out = blockText(c.State.Content)
					}
					for i := range evs {
						evs[i].CallID = c.ID
						opencodeResult(&evs[i], c.State.Status, out, args(c.State.Metadata))
					}
					s.add(tt, evs...)
				default:
					s.touch(t)
				}
			}
			if d.Tokens != nil {
				if ctx := d.Tokens.Input + d.Tokens.Cache.Read + d.Tokens.Cache.Write; ctx > 0 {
					s.Ctx = ctx
					s.CtxMax = growMax(s.CtxMax, ctx)
				}
			}
			s.touch(t)
		case "idle":
			text := ""
			if d.Outcome != "" && d.Outcome != "succeeded" {
				text = d.Outcome
			}
			s.endTurn(t, text)
		}
	}
}

func opencodeResult(e *Event, status, out string, meta args) {
	switch status {
	case "completed":
		e.Result = OK
	case "error":
		e.Result = Failed
	default:
		return
	}
	switch e.Kind {
	case KindRun:
		exit := -1
		if n, ok := meta.num("exit"); ok {
			exit = n
		}
		if o := meta.str("output"); o != "" {
			out = o
		}
		runResult(e, out, exit, status == "error")
	case KindGrep:
		if n, ok := meta.num("matches", "count"); ok {
			e.Hits = n
		}
	case KindEdit:
		files, _ := meta["files"].([]any)
		for _, f := range files {
			m, _ := f.(map[string]any)
			p := args(m).str("filePath", "file")
			if p != "" && (p == e.File || strings.HasSuffix(e.File, "/"+p)) {
				e.Add, _ = args(m).num("additions")
				e.Del, _ = args(m).num("deletions")
			}
		}
	}
}

// growMax picks a context window size that holds ctx tokens.
func growMax(cur, ctx int) int {
	for _, n := range []int{200_000, 272_000, 400_000, 1_000_000, 2_000_000} {
		if n >= cur && n >= ctx {
			return n
		}
	}
	return ctx
}
