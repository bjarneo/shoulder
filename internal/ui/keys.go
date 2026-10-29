package ui

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/bjarneo/shoulder/internal/config"
)

// action is a command that a key sequence runs.
type action int

const (
	actNone action = iota
	actAgentNext
	actAgentPrev
	actAuto
	actFollow
	actBack
	actDown
	actUp
	actPageDown
	actPageUp
	actTop
	actBottom
	actHunkNext
	actHunkPrev
	actBufferNext
	actBufferPrev
	actSplits
	actInlineDiff
	actReload
	actHelp
	actQuit
)

// actions lists the actions that the [keys] table of the config file can
// bind, with their default keys. A sequence has its keys in one string with
// spaces between them.
var actions = []struct {
	act  action
	name string
	keys []string
}{
	{actAgentNext, "agent_next", []string{"tab"}},
	{actAgentPrev, "agent_prev", []string{"shift+tab"}},
	{actAuto, "auto", []string{"a"}},
	{actFollow, "follow", []string{"f"}},
	{actBack, "back", []string{"esc"}},
	{actDown, "down", []string{"j", "down"}},
	{actUp, "up", []string{"k", "up"}},
	{actPageDown, "page_down", []string{"ctrl+d", "pgdown"}},
	{actPageUp, "page_up", []string{"ctrl+u", "pgup"}},
	{actTop, "top", []string{"g g", "home"}},
	{actBottom, "bottom", []string{"G", "end"}},
	{actHunkNext, "hunk_next", []string{"] c"}},
	{actHunkPrev, "hunk_prev", []string{"[ c"}},
	{actBufferNext, "buffer_next", []string{"L", "g t"}},
	{actBufferPrev, "buffer_prev", []string{"H", "g T"}},
	{actSplits, "splits", []string{"s"}},
	{actInlineDiff, "inline_diff", []string{"d"}},
	{actReload, "reload", []string{"R"}},
	{actHelp, "help", []string{"?"}},
	{actQuit, "quit", []string{"q"}},
}

// Keymap maps key sequences to actions.
type Keymap struct {
	seqs map[string]action   // a sequence, with spaces between the keys
	pre  map[string]bool     // the sequences that start a longer sequence
	keys map[action][]string // the sequences of each action, in order
}

// NewKeymap makes the keymap from the bindings of the config file. An
// action that the config binds loses its default keys. A default key that a
// binding uses, or that starts like one, goes away.
func NewKeymap(binds []config.Binding) (*Keymap, error) {
	byName := map[string]action{}
	names := make([]string, 0, len(actions))
	for _, a := range actions {
		byName[a.name] = a.act
		names = append(names, a.name)
	}
	user := map[action][]string{}
	owner := map[string]config.Binding{}
	for _, b := range binds {
		act, ok := byName[b.Action]
		if !ok {
			return nil, fmt.Errorf("line %d: unknown action %q. The actions are %s", b.Line, b.Action, strings.Join(names, ", "))
		}
		seqs := []string{}
		for _, k := range b.Keys {
			seq, err := parseSeq(k)
			if err != nil {
				return nil, fmt.Errorf("line %d: %s: %v", b.Line, b.Action, err)
			}
			first, _, _ := strings.Cut(seq, " ")
			if (len(first) == 1 && first >= "1" && first <= "9") || slices.Contains(strings.Fields(seq), "ctrl+c") {
				return nil, fmt.Errorf("line %d: %s: 1 … 9 pin agents and ctrl+c quits, so you cannot bind %q", b.Line, b.Action, k)
			}
			for s, o := range owner {
				switch {
				case s == seq && o.Action == b.Action:
				case s == seq:
					return nil, fmt.Errorf("line %d: %s: %s is already bound to %s on line %d", b.Line, b.Action, seqLabel(seq), o.Action, o.Line)
				case startsWith(s, seq) || startsWith(seq, s):
					return nil, fmt.Errorf("line %d: %s: %s and %s on line %d start with the same keys", b.Line, b.Action, seqLabel(seq), seqLabel(s), o.Line)
				}
			}
			if !slices.Contains(seqs, seq) {
				seqs = append(seqs, seq)
			}
			owner[seq] = b
		}
		user[act] = seqs
	}
	km := &Keymap{seqs: map[string]action{}, pre: map[string]bool{}, keys: map[action][]string{}}
	for _, a := range actions {
		seqs, ok := user[a.act]
		if !ok {
			for _, d := range a.keys {
				clash := false
				for s := range owner {
					clash = clash || s == d || startsWith(s, d) || startsWith(d, s)
				}
				if !clash {
					seqs = append(seqs, d)
				}
			}
		}
		km.keys[a.act] = seqs
		for _, s := range seqs {
			km.seqs[s] = a.act
			for i := range len(s) {
				if s[i] == ' ' {
					km.pre[s[:i]] = true
				}
			}
		}
	}
	return km, nil
}

// startsWith reports whether the sequence a starts with all keys of the
// shorter sequence b.
func startsWith(a, b string) bool { return strings.HasPrefix(a, b+" ") }

var (
	namedKeys = map[string]bool{
		"enter": true, "tab": true, "backspace": true, "esc": true, "space": true,
		"up": true, "down": true, "left": true, "right": true, "insert": true,
		"delete": true, "home": true, "end": true, "pgup": true, "pgdown": true,
	}
	modOrder = []string{"ctrl", "alt", "shift", "meta", "hyper", "super"}
)

// parseSeq reads a key sequence from the config file. Spaces separate the
// keys. A word of 1 or 2 characters, such as "n" or "gg", gives 1 key for
// each character. A longer word must be one key, such as "pgdown" or
// "ctrl+n".
func parseSeq(s string) (string, error) {
	var keys []string
	for _, f := range strings.Fields(s) {
		if k, ok := keyName(f); ok {
			keys = append(keys, k)
			continue
		}
		if utf8.RuneCountInString(f) > 2 {
			return "", fmt.Errorf("unknown key %q. Put spaces between the keys of a sequence, for example \"g g g\"", f)
		}
		for _, r := range f {
			keys = append(keys, string(r))
		}
	}
	if len(keys) == 0 {
		return "", errors.New("the key is empty")
	}
	return strings.Join(keys, " "), nil
}

// keyName reads one named key, with or without modifiers, in the form that
// Bubble Tea gives a key press.
func keyName(f string) (string, bool) {
	parts := strings.Split(f, "+")
	base := parts[len(parts)-1]
	mods := map[string]bool{}
	for _, p := range parts[:len(parts)-1] {
		p = strings.ToLower(p)
		if !slices.Contains(modOrder, p) || mods[p] {
			return "", false
		}
		mods[p] = true
	}
	switch r, n := utf8.DecodeRuneInString(base); {
	case namedKeys[strings.ToLower(base)]:
		base = strings.ToLower(base)
	case len(mods) > 0 && n == len(base) && r != utf8.RuneError:
		// Bubble Tea names a shifted letter by the letter, and a letter
		// with ctrl or alt in lower case.
		if len(mods) == 1 && mods["shift"] && unicode.IsLetter(r) {
			return string(unicode.ToUpper(r)), true
		}
		base = string(unicode.ToLower(r))
	case len(base) > 1 && (base[0] == 'f' || base[0] == 'F') && isDigits(base[1:]):
		base = strings.ToLower(base)
	default:
		return "", false
	}
	var b strings.Builder
	for _, m := range modOrder {
		if mods[m] {
			b.WriteString(m + "+")
		}
	}
	return b.String() + base, true
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// keyLabels shortens key names for the screen.
var keyLabels = map[string]string{
	"up": "↑", "down": "↓", "left": "←", "right": "→", "pgdown": "pgdn",
}

// seqLabel returns the short form of a sequence, such as "gg" or "C-w j".
func seqLabel(seq string) string {
	keys := strings.Fields(seq)
	short := true
	for i, k := range keys {
		if l, ok := keyLabels[k]; ok {
			k = l
		}
		k = strings.NewReplacer("ctrl+", "C-", "alt+", "M-", "shift+", "S-").Replace(k)
		keys[i] = k
		short = short && utf8.RuneCountInString(k) == 1
	}
	if short {
		return strings.Join(keys, "")
	}
	return strings.Join(keys, " ")
}

// label returns the keys of an action for the screen, or "" when the
// action has no keys.
func (k *Keymap) label(a action) string {
	var out []string
	for _, s := range k.keys[a] {
		out = append(out, seqLabel(s))
	}
	return strings.Join(out, " ")
}

// first returns the first key of an action for the screen, or "".
func (k *Keymap) first(a action) string {
	if s := k.keys[a]; len(s) > 0 {
		return seqLabel(s[0])
	}
	return ""
}

// helpRows lists the rows of the help screen. A row has 1 action, or 2
// actions that go in opposite directions. The keys of 1 … 9 do not change.
var helpRows = []struct {
	acts []action
	text string
}{
	{nil, "pin an agent and follow it"},
	{[]action{actAgentNext, actAgentPrev}, "pin the next / previous agent"},
	{[]action{actAuto}, "auto focus: jump to new edits ↔ stay"},
	{[]action{actFollow}, "toggle follow / free scroll"},
	{[]action{actBack}, "back to follow"},
	{[]action{actDown, actUp}, "move the cursor down / up"},
	{[]action{actPageDown, actPageUp}, "half page down / up"},
	{[]action{actTop, actBottom}, "top / bottom of buffer"},
	{[]action{actHunkNext, actHunkPrev}, "next / previous hunk"},
	{[]action{actBufferNext, actBufferPrev}, "next / previous buffer"},
	{[]action{actSplits}, "follow layout ↔ one split per agent"},
	{[]action{actInlineDiff}, "inline diff ↔ signs"},
	{[]action{actReload}, "read all sessions again"},
	{[]action{actQuit}, "quit"},
}

// help returns the rows of the help screen for this keymap.
func (k *Keymap) help() [][2]string {
	var out [][2]string
	for _, r := range helpRows {
		if r.acts == nil {
			out = append(out, [2]string{"1 … 9", r.text})
			continue
		}
		var parts []string
		bound := false
		for _, a := range r.acts {
			l := k.label(a)
			if l == "" {
				l = "–"
			} else {
				bound = true
			}
			parts = append(parts, l)
		}
		if bound {
			out = append(out, [2]string{strings.Join(parts, " / "), r.text})
		}
	}
	return out
}

// sidebar returns the short key list of the sidebar for this keymap.
func (k *Keymap) sidebar() [][2]string {
	join := func(s ...string) string {
		return strings.Join(slices.DeleteFunc(s, func(x string) bool { return x == "" }), " ")
	}
	rows := [][2]string{
		{join("1-9", k.first(actAgentNext)), "pin agent"},
		{k.first(actAuto), "auto focus"},
		{join(k.first(actFollow), k.first(actBack)), "free scroll / follow"},
		{join(k.first(actDown), k.first(actUp), k.first(actTop), k.first(actBottom)), "move"},
		{join(k.first(actHunkNext), k.first(actHunkPrev)), "next / prev hunk"},
		{join(k.first(actBufferPrev), k.first(actBufferNext)), "prev / next buffer"},
		{k.first(actSplits), "split per agent"},
		{k.first(actHelp), "all keys"},
	}
	return slices.DeleteFunc(rows, func(r [2]string) bool { return r[0] == "" })
}
