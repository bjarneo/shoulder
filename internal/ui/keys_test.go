package ui

import (
	"strings"
	"testing"

	"github.com/bjarneo/shoulder/internal/config"
)

func TestParseSeq(t *testing.T) {
	cases := map[string]string{
		"n":           "n",
		"gg":          "g g",
		"]c":          "] c",
		"g g g":       "g g g",
		"ctrl+n":      "ctrl+n",
		"Ctrl+N":      "ctrl+n",
		"shift+alt+x": "alt+shift+x",
		"shift+g":     "G",
		"shift+tab":   "shift+tab",
		"PgDown":      "pgdown",
		"F5":          "f5",
		"ctrl+w j":    "ctrl+w j",
		"space":       "space",
		"+":           "+",
	}
	for in, want := range cases {
		got, err := parseSeq(in)
		if err != nil || got != want {
			t.Errorf("%q: got %q, %v, want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "pgdwn", "ctrl+", "super+duper+x", "ggg"} {
		if got, err := parseSeq(in); err == nil {
			t.Errorf("%q: got %q, want an error", in, got)
		}
	}
}

func TestKeymapDefaults(t *testing.T) {
	k, err := NewKeymap(nil)
	if err != nil {
		t.Fatal(err)
	}
	for seq, want := range map[string]action{
		"g g": actTop, "g t": actBufferNext, "] c": actHunkNext, "[ c": actHunkPrev,
		"j": actDown, "down": actDown, "ctrl+d": actPageDown, "q": actQuit, "?": actHelp,
	} {
		if k.seqs[seq] != want {
			t.Errorf("%q: got %v, want %v", seq, k.seqs[seq], want)
		}
	}
	if !k.pre["g"] || !k.pre["]"] || k.pre["g g"] {
		t.Errorf("prefixes: %v", k.pre)
	}
	// The sidebar keeps the labels of the design.
	want := [][2]string{
		{"1-9 tab", "pin agent"}, {"a", "auto focus"}, {"f esc", "free scroll / follow"},
		{"j k gg G", "move"}, {"]c [c", "next / prev hunk"}, {"H L", "prev / next buffer"},
		{"s", "split per agent"}, {"?", "all keys"},
	}
	if got := k.sidebar(); len(got) != len(want) {
		t.Fatalf("sidebar: %v", got)
	} else {
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("sidebar row %d: got %v, want %v", i, got[i], want[i])
			}
		}
	}
}

func TestKeymapOverrides(t *testing.T) {
	k, err := NewKeymap([]config.Binding{
		{Action: "hunk_next", Keys: []string{"n"}, Line: 2},
		{Action: "hunk_prev", Keys: []string{"p"}, Line: 3},
		{Action: "buffer_next", Keys: []string{"s"}, Line: 4},
		{Action: "top", Keys: []string{"g"}, Line: 5},
		{Action: "reload", Keys: []string{}, Line: 6},
	})
	if err != nil {
		t.Fatal(err)
	}
	checks := map[string]action{
		"n": actHunkNext, "p": actHunkPrev, "] c": actNone,
		// buffer_next takes s from splits, so splits has no key.
		"s": actBufferNext,
		// g alone is top now, so the g sequences of the defaults go away.
		"g": actTop, "g g": actNone, "g t": actNone, "L": actNone, "H": actBufferPrev,
		"R": actNone,
	}
	for seq, want := range checks {
		if k.seqs[seq] != want {
			t.Errorf("%q: got %v, want %v", seq, k.seqs[seq], want)
		}
	}
	if k.pre["g"] {
		t.Error("g still starts a sequence")
	}
	help := map[string]string{}
	for _, r := range k.help() {
		help[r[1]] = r[0]
	}
	if help["next / previous hunk"] != "n / p" || help["top / bottom of buffer"] != "g / G end" {
		t.Errorf("help: %v", help)
	}
	if _, ok := help["read all sessions again"]; ok {
		t.Error("the help lists an action without keys")
	}
	if _, ok := help["follow layout ↔ one split per agent"]; ok {
		t.Error("the help lists splits, which has no key now")
	}
}

func TestKeymapErrors(t *testing.T) {
	cases := []struct {
		binds []config.Binding
		want  string
	}{
		{[]config.Binding{{Action: "hunk_nxt", Keys: []string{"n"}, Line: 2}}, `line 2: unknown action "hunk_nxt"`},
		{[]config.Binding{{Action: "quit", Keys: []string{"pgdwn"}, Line: 2}}, `line 2: quit: unknown key "pgdwn"`},
		{[]config.Binding{{Action: "quit", Keys: []string{"1"}, Line: 2}}, "line 2: quit: 1 … 9 pin agents"},
		{[]config.Binding{{Action: "quit", Keys: []string{"ctrl+c"}, Line: 2}}, "line 2: quit: 1 … 9 pin agents"},
		{[]config.Binding{
			{Action: "hunk_next", Keys: []string{"n"}, Line: 2},
			{Action: "hunk_prev", Keys: []string{"n"}, Line: 3},
		}, "line 3: hunk_prev: n is already bound to hunk_next on line 2"},
		{[]config.Binding{
			{Action: "top", Keys: []string{"g"}, Line: 2},
			{Action: "bottom", Keys: []string{"gb"}, Line: 3},
		}, "line 3: bottom: gb and g on line 2 start with the same keys"},
	}
	for _, c := range cases {
		_, err := NewKeymap(c.binds)
		if err == nil || !strings.HasPrefix(err.Error(), c.want) {
			t.Errorf("got %v, want %q", err, c.want)
		}
	}
}

func TestRebindKeys(t *testing.T) {
	m, _ := fixture(t)
	k, err := NewKeymap([]config.Binding{
		{Action: "hunk_next", Keys: []string{"n"}},
		{Action: "top", Keys: []string{"ctrl+w g"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	m.keys, m.side, m.helpTab = k, k.sidebar(), k.help()
	m.key("f")
	m.key("G")
	end := m.view.ucur
	m.key("ctrl+w")
	if m.pending != "ctrl+w" {
		t.Fatalf("pending: %q", m.pending)
	}
	m.key("g")
	if m.pending != "" || m.view.ucur != 0 {
		t.Fatalf("ctrl+w g: pending %q, line %d", m.pending, m.view.ucur)
	}
	m.key("n")
	if m.view.ucur == 0 || m.view.ucur == end {
		t.Fatalf("n did not go to the next hunk: line %d", m.view.ucur)
	}
	// A key that does not continue the sequence ends it.
	m.key("ctrl+w")
	m.key("x")
	if m.pending != "" {
		t.Fatalf("pending: %q", m.pending)
	}
	checkFrame(t, m, 180, 50, "rebound")
	m.key("?")
	if out := checkFrame(t, m, 180, 50, "rebound-help"); !strings.Contains(out, "C-w g") {
		t.Error("the help does not show the new key")
	}
}
