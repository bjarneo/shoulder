package watch

import (
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// shellEvents classifies a shell command. A file read becomes a read event
// and a search becomes a grep event, so that the file view can follow agents
// that read through the shell. A command with more parts, such as
// "cat a.go && sed -n 1,80p b.go", gives an event for each read and search,
// and a run event for the other parts. The run event comes last, so that
// the agent shows as running while the command runs. It keeps the command
// and its output. Without a run event, the first event keeps them.
//
// The reads and searches in a subshell, such as "(cd sub && cat a.go)", and
// in a command substitution, such as "$(cat a.go)", also give events. A cd
// in a subshell does not change the directory of the commands after it.
func shellEvents(e Event, cmd, cwd string) []Event {
	e.Cmd = strings.TrimSpace(cmd)
	e.Kind = KindRun
	if strings.Contains(e.Cmd, "<<") {
		// The lines of a here-document are text, not commands.
		return []Event{e}
	}
	list, ok := parseScript(e.Cmd)
	if !ok {
		return []Event{e}
	}
	r := shellRun{e: e}
	d := shDir{cwd: cwd}
	r.list(list, &d)
	out := r.out
	if len(out) == 0 {
		return []Event{e}
	}
	for i := range out {
		if r.other || i > 0 {
			out[i].Cmd = ""
		}
	}
	if r.other {
		out = append(out, e)
	}
	return out
}

// quietCmds are commands that only print text, such as a separator between
// two reads.
var quietCmds = map[string]bool{"echo": true, "printf": true, "pwd": true, "true": true, ":": true}

// filterCmds can follow a read or a search in a pipe.
var filterCmds = map[string]bool{"head": true, "tail": true, "sort": true, "uniq": true, "wc": true, "cut": true}

// shellKeywords are reserved words that can come before a command, or stand
// alone at the end of a block. The reader skips them.
var shellKeywords = map[string]bool{
	"if": true, "then": true, "elif": true, "else": true, "fi": true, "while": true, "until": true,
	"do": true, "done": true, "esac": true, "{": true, "}": true, "!": true, "time": true,
}

// shellBlocks start a command that is not a simple command.
var shellBlocks = map[string]bool{"for": true, "case": true, "select": true, "function": true, "coproc": true}

// shellPart classifies one pipe of a script. segs holds the words of each
// command in the pipe. It reports false for a pipe that is not a read or a
// search.
func shellPart(e Event, segs [][]string, cwd string) (Event, bool) {
	if len(segs) == 0 || len(segs) > 3 || len(segs[0]) == 0 {
		return e, false
	}
	words := segs[0]
	tool := filepath.Base(words[0])
	rest := 1
	if len(segs) > 1 && (tool == "nl" || tool == "cat") {
		// "nl -ba f | sed -n '1,80p'" and "cat -n f | sed -n '1,80p'"
		if from, to, ok := sedRange(segs[1]); ok {
			if p, ok := shellPath(lastNonFlag(words[1:]), cwd); ok {
				e = readEvent(e, p, from, to)
				rest = 2
			}
		}
	}
	for i := rest; i < len(segs); i++ {
		if len(segs[i]) == 0 || !filterCmds[filepath.Base(segs[i][0])] {
			return e, false
		}
	}
	if rest == 2 {
		return e, true
	}
	switch tool {
	case "cat", "bat", "less":
		if files := nonFlags(words[1:]); len(files) == 1 {
			if p, ok := shellPath(files[0], cwd); ok {
				return readEvent(e, p, 0, -1), true
			}
		}
	case "sed":
		if from, to, ok := sedRange(words); ok {
			if file := lastNonFlag(words[1:]); !sedExpr.MatchString(file) {
				if p, ok := shellPath(file, cwd); ok {
					return readEvent(e, p, from, to), true
				}
			}
		}
	case "head":
		n, file := 10, ""
		for i := 1; i < len(words); i++ {
			w := words[i]
			switch {
			case w == "-n" && i+1 < len(words):
				n, _ = strconv.Atoi(words[i+1])
				i++
			case strings.HasPrefix(w, "-n"):
				n, _ = strconv.Atoi(w[2:])
			case strings.HasPrefix(w, "-") && len(w) > 1:
				if v, err := strconv.Atoi(w[1:]); err == nil {
					n = v
				}
			default:
				file = w
			}
		}
		if p, ok := shellPath(file, cwd); ok && n > 0 {
			return readEvent(e, p, 0, n-1), true
		}
	case "rg", "grep", "ag", "ack":
		if pat, where := grepArgs(words[1:]); pat != "" {
			e.Kind, e.Query, e.Text = KindGrep, pat, where
			return e, true
		}
	case "git":
		if len(words) > 2 && words[1] == "grep" {
			if pat, where := grepArgs(words[2:]); pat != "" {
				e.Kind, e.Query, e.Text = KindGrep, pat, where
				return e, true
			}
		}
	}
	return e, false
}

// shellPath resolves a file name of a shell command. It reports false for
// an empty name, and for a relative name when the directory is unknown.
func shellPath(p, cwd string) (string, bool) {
	switch {
	case p == "":
		return "", false
	case p == "~":
		return home(), true
	case strings.HasPrefix(p, "~/") || filepath.IsAbs(p):
		return absPath(p, cwd), true
	case cwd == "":
		return "", false
	}
	return absPath(p, cwd), true
}

// shWord is one word of a command after the shell removes the quotes.
type shWord struct {
	text string
	dyn  bool // the shell expands a part of the word, such as $HOME or $(...)
}

// shCmd is one command of a pipe: a simple command or a ( ... ) subshell.
type shCmd struct {
	words  []shWord
	unsafe bool       // a redirection reads a file, or writes a file other than /dev/null
	group  []shPipe   // the commands of a ( ... ) subshell
	isSub  bool       // the command is a ( ... ) subshell
	inner  [][]shPipe // command and process substitutions, which run first
}

func (c *shCmd) empty() bool { return len(c.words) == 0 && !c.isSub && len(c.inner) == 0 }

// shPipe is one or more commands that a pipe joins.
type shPipe []*shCmd

// maxShellDepth limits the nesting of subshells and substitutions.
const maxShellDepth = 32

// shParser reads a script into lists of pipes. It knows quotes, backslash
// escapes, comments, redirections, subshells and substitutions. It reports
// false for a syntax that it does not know, such as a function definition.
type shParser struct {
	s     string
	i     int
	depth int
}

func parseScript(s string) ([]shPipe, bool) {
	p := &shParser{s: s}
	list, ok := p.list(false)
	if !ok || p.i < len(p.s) {
		return nil, false
	}
	return list, true
}

func (p *shParser) more() bool { return p.i < len(p.s) }

func (p *shParser) at(c byte) bool { return p.i < len(p.s) && p.s[p.i] == c }

// skip moves past the bytes in chars and past escaped newlines.
func (p *shParser) skip(chars string) {
	for p.i < len(p.s) {
		switch {
		case strings.IndexByte(chars, p.s[p.i]) >= 0:
			p.i++
		case p.s[p.i] == '\\' && p.i+1 < len(p.s) && p.s[p.i+1] == '\n':
			p.i += 2
		default:
			return
		}
	}
}

// list reads pipes and the separators between them. When closing is true,
// it stops at a ")" and leaves it for the caller. Otherwise it reads to the
// end of the text.
func (p *shParser) list(closing bool) ([]shPipe, bool) {
	p.depth++
	defer func() { p.depth-- }()
	if p.depth > maxShellDepth {
		return nil, false
	}
	var out []shPipe
	for {
		p.skip(" \t\r\n;&|")
		if !p.more() {
			return out, !closing
		}
		if p.at(')') {
			return out, closing
		}
		pipe, ok := p.pipe()
		if !ok {
			return nil, false
		}
		out = append(out, pipe)
	}
}

// pipe reads commands that "|" or "|&" join.
func (p *shParser) pipe() (shPipe, bool) {
	var out shPipe
	for {
		c, ok := p.cmd()
		if !ok {
			return nil, false
		}
		out = append(out, c)
		if !p.at('|') || (p.i+1 < len(p.s) && p.s[p.i+1] == '|') {
			return out, true
		}
		p.i++
		if p.at('&') {
			p.i++
		}
	}
}

// cmd reads one command. It stops at a separator, a pipe or a ")".
func (p *shParser) cmd() (*shCmd, bool) {
	c := &shCmd{}
	p.skip(" \t\r")
	if p.at('(') {
		p.i++
		group, ok := p.list(true)
		if !ok || !p.at(')') {
			return nil, false
		}
		p.i++
		c.group, c.isSub = group, true
	}
	for {
		p.skip(" \t\r")
		if !p.more() {
			return c, true
		}
		ch := p.s[p.i]
		next := byte(0)
		if p.i+1 < len(p.s) {
			next = p.s[p.i+1]
		}
		switch {
		case ch == ';' || ch == '\n' || ch == '|' || ch == ')':
			return c, true
		case ch == '&' && next != '>':
			return c, true
		case ch == '(':
			return nil, false
		case (ch == '<' || ch == '>') && next == '(':
			// A process substitution, such as "<(sort a.txt)".
			p.i += 2
			group, ok := p.list(true)
			if !ok || !p.at(')') {
				return nil, false
			}
			p.i++
			c.inner = append(c.inner, group)
			c.words = append(c.words, shWord{dyn: true})
		case ch == '<' || ch == '>' || ch == '&':
			if !p.redirect(c) {
				return nil, false
			}
		default:
			if c.isSub && ch != '#' {
				return nil, false
			}
			start := p.i
			w, ok := p.word(c)
			if !ok {
				return nil, false
			}
			if w == nil {
				continue
			}
			if (p.at('<') || p.at('>')) && isDigits(p.s[start:p.i]) {
				// A number before "<" or ">" is the file descriptor of a
				// redirection, such as "2>/dev/null".
				if !p.redirect(c) {
					return nil, false
				}
				continue
			}
			c.words = append(c.words, *w)
		}
	}
}

// redirect reads one redirection and its target. It marks the command as
// unsafe when the redirection reads a file, or writes a file other than
// /dev/null.
func (p *shParser) redirect(c *shCmd) bool {
	rest := p.s[p.i:]
	op := ""
	for _, o := range []string{"&>>", "&>", ">>", ">&", ">|", "<&", "<>", ">", "<"} {
		if strings.HasPrefix(rest, o) {
			op = o
			break
		}
	}
	if op == "" || strings.HasPrefix(rest, "<<") {
		return false
	}
	p.i += len(op)
	p.skip(" \t")
	if !p.more() {
		return false
	}
	w, ok := p.word(c)
	if !ok || w == nil {
		return false
	}
	switch {
	case w.dyn:
		c.unsafe = true
	case (op == ">&" || op == "<&") && (isDigits(w.text) || w.text == "-"):
		// "2>&1" copies a file descriptor.
	case op[0] != '<' && w.text == "/dev/null":
		// "2>/dev/null" hides output.
	default:
		c.unsafe = true
	}
	return true
}

// word reads one word and removes its quotes. It returns nil for a comment.
func (p *shParser) word(c *shCmd) (*shWord, bool) {
	if p.at('#') {
		for p.more() && !p.at('\n') {
			p.i++
		}
		return nil, true
	}
	w := &shWord{}
	var b strings.Builder
	quoted := false
	for p.more() {
		ch := p.s[p.i]
		switch ch {
		case ' ', '\t', '\r', '\n', ';', '&', '|', '<', '>', '(', ')':
			return p.endWord(w, &b, quoted)
		case '\\':
			p.i++
			switch {
			case !p.more():
				b.WriteByte('\\')
			case p.at('\n'):
				// An escaped newline joins two lines.
				p.i++
			default:
				b.WriteByte(p.s[p.i])
				p.i++
			}
		case '\'':
			j := strings.IndexByte(p.s[p.i+1:], '\'')
			if j < 0 {
				return nil, false
			}
			b.WriteString(p.s[p.i+1 : p.i+1+j])
			p.i += j + 2
			quoted = true
		case '"':
			p.i++
			if !p.dquote(c, w, &b) {
				return nil, false
			}
			quoted = true
		case '$':
			if !p.dollar(c, w, &b, false) {
				return nil, false
			}
		case '`':
			if !p.backtick(c, w) {
				return nil, false
			}
		default:
			b.WriteByte(ch)
			p.i++
		}
	}
	return p.endWord(w, &b, quoted)
}

func (p *shParser) endWord(w *shWord, b *strings.Builder, quoted bool) (*shWord, bool) {
	w.text = b.String()
	if w.text == "" && !quoted && !w.dyn {
		return nil, true
	}
	return w, true
}

// dquote reads the text of a double quote, after the opening quote.
func (p *shParser) dquote(c *shCmd, w *shWord, b *strings.Builder) bool {
	for p.more() {
		ch := p.s[p.i]
		switch ch {
		case '"':
			p.i++
			return true
		case '\\':
			if p.i+1 < len(p.s) {
				switch n := p.s[p.i+1]; n {
				case '$', '`', '"', '\\':
					b.WriteByte(n)
					p.i += 2
					continue
				case '\n':
					p.i += 2
					continue
				}
			}
			b.WriteByte('\\')
			p.i++
		case '$':
			if !p.dollar(c, w, b, true) {
				return false
			}
		case '`':
			if !p.backtick(c, w) {
				return false
			}
		default:
			b.WriteByte(ch)
			p.i++
		}
	}
	return false
}

// dollar reads an expansion that starts with "$". A "$" that starts no
// expansion is text.
func (p *shParser) dollar(c *shCmd, w *shWord, b *strings.Builder, inQuote bool) bool {
	rest := p.s[p.i+1:]
	switch {
	case strings.HasPrefix(rest, "(("):
		// Arithmetic, such as "$((n+1))". It runs no command.
		depth := 0
		for j := p.i + 1; j < len(p.s); j++ {
			switch p.s[j] {
			case '(':
				depth++
			case ')':
				depth--
				if depth == 0 {
					p.i = j + 1
					w.dyn = true
					return true
				}
			}
		}
		return false
	case strings.HasPrefix(rest, "("):
		p.i += 2
		group, ok := p.list(true)
		if !ok || !p.at(')') {
			return false
		}
		p.i++
		c.inner = append(c.inner, group)
		w.dyn = true
	case strings.HasPrefix(rest, "{"):
		j := strings.IndexByte(rest, '}')
		if j < 0 {
			return false
		}
		p.i += j + 2
		w.dyn = true
	case !inQuote && strings.HasPrefix(rest, "'"):
		// ANSI-C quotes, such as $'a\tb'.
		j := 1
		for j < len(rest) && rest[j] != '\'' {
			if rest[j] == '\\' {
				j++
			}
			j++
		}
		if j >= len(rest) {
			return false
		}
		p.i += j + 2
		w.dyn = true
	case rest != "" && (rest[0] == '_' || isLetter(rest[0])):
		j := 1
		for j < len(rest) && (rest[j] == '_' || isLetter(rest[j]) || isDigit(rest[j])) {
			j++
		}
		p.i += j + 1
		w.dyn = true
	case rest != "" && strings.IndexByte("?@*#!$-0123456789", rest[0]) >= 0:
		p.i += 2
		w.dyn = true
	default:
		b.WriteByte('$')
		p.i++
	}
	return true
}

// backtick reads an old-style command substitution, such as `cat a.go`.
func (p *shParser) backtick(c *shCmd, w *shWord) bool {
	var inner strings.Builder
	for j := p.i + 1; j < len(p.s); j++ {
		switch ch := p.s[j]; ch {
		case '\\':
			if j+1 < len(p.s) && strings.IndexByte("$`\\", p.s[j+1]) >= 0 {
				inner.WriteByte(p.s[j+1])
				j++
				continue
			}
			inner.WriteByte(ch)
		case '`':
			sub := &shParser{s: inner.String(), depth: p.depth}
			group, ok := sub.list(false)
			if !ok {
				return false
			}
			p.i = j + 1
			c.inner = append(c.inner, group)
			w.dyn = true
			return true
		default:
			inner.WriteByte(ch)
		}
	}
	return false
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isDigit(s[i]) {
			return false
		}
	}
	return true
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isLetter(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }

// shDir is the working directory while a script runs. An empty directory
// is unknown, for example after "cd $DIR".
type shDir struct {
	cwd   string
	prev  string // the directory before the last cd, for "cd -"
	stack []string
}

// sub returns a copy for a subshell, which does not change d.
func (d *shDir) sub() shDir {
	c := *d
	c.stack = slices.Clone(d.stack)
	return c
}

// cd changes the directory for cd, pushd and popd with the arguments args.
func (d *shDir) cd(name string, args []shWord) {
	var rest []shWord
	for _, a := range args {
		if a.dyn || a.text == "-" || !strings.HasPrefix(a.text, "-") {
			rest = append(rest, a)
		}
	}
	old := d.cwd
	defer func() { d.prev = old }()
	switch name {
	case "popd":
		if n := len(d.stack); n > 0 && len(rest) == 0 {
			d.cwd, d.stack = d.stack[n-1], d.stack[:n-1]
		} else {
			d.cwd = ""
		}
		return
	case "pushd":
		if len(rest) == 0 {
			if n := len(d.stack); n > 0 {
				d.cwd, d.stack[n-1] = d.stack[n-1], d.cwd
			} else {
				d.cwd = ""
			}
			return
		}
		d.stack = append(d.stack, old)
	}
	switch {
	case len(rest) == 0:
		d.cwd = home()
	case len(rest) > 1 || rest[0].dyn || strings.HasPrefix(rest[0].text, "+"):
		d.cwd = ""
	case rest[0].text == "-":
		d.cwd = d.prev
	default:
		d.cwd, _ = shellPath(rest[0].text, old)
	}
}

// shellRun collects the events of a script.
type shellRun struct {
	e     Event
	out   []Event
	other bool // a part of the script is not a read or a search
}

func (r *shellRun) list(list []shPipe, d *shDir) {
	for _, p := range list {
		r.pipe(p, d)
	}
}

func (r *shellRun) pipe(p shPipe, d *shDir) {
	var cmds []*shCmd
	for _, c := range p {
		// A substitution runs in a subshell before its command.
		for _, in := range c.inner {
			sd := d.sub()
			r.list(in, &sd)
		}
		if !c.empty() {
			cmds = append(cmds, c)
		}
	}
	if len(cmds) == 0 {
		return
	}
	isSub := false
	for _, c := range cmds {
		if c.isSub {
			isSub = true
			sd := d.sub()
			r.list(c.group, &sd)
		}
	}
	if isSub {
		if len(cmds) > 1 || cmds[0].unsafe {
			r.other = true
		}
		return
	}
	words := make([][]shWord, len(cmds))
	for i, c := range cmds {
		words[i] = c.words
		if i == 0 {
			for len(words[i]) > 0 && !words[i][0].dyn && shellKeywords[words[i][0].text] {
				words[i] = words[i][1:]
			}
		}
		words[i] = assignments(words[i])
	}
	if len(cmds) == 1 {
		w := words[0]
		switch {
		case len(w) == 0:
			// A command with only keywords or assignments, such as "fi".
			if cmds[0].unsafe {
				r.other = true
			}
			return
		case w[0].dyn:
		case w[0].text == "cd" || w[0].text == "pushd" || w[0].text == "popd":
			d.cd(w[0].text, w[1:])
			if cmds[0].unsafe {
				r.other = true
			}
			return
		case quietCmds[w[0].text] && !cmds[0].unsafe:
			return
		}
	}
	segs := make([][]string, len(cmds))
	for i, c := range cmds {
		if c.unsafe || len(words[i]) == 0 || shellBlocks[words[i][0].text] {
			r.other = true
			return
		}
		for _, w := range words[i] {
			if w.dyn {
				r.other = true
				return
			}
			segs[i] = append(segs[i], w.text)
		}
	}
	if x, ok := shellPart(r.e, segs, d.cwd); ok {
		r.out = append(r.out, x)
	} else {
		r.other = true
	}
}

// assignments returns the words after the variable assignments at the
// start of a command, such as "LC_ALL=C grep x".
func assignments(words []shWord) []shWord {
	for len(words) > 0 && isAssignment(words[0].text) {
		words = words[1:]
	}
	return words
}

func isAssignment(s string) bool {
	i := strings.IndexByte(s, '=')
	if i <= 0 || !(s[0] == '_' || isLetter(s[0])) {
		return false
	}
	for j := 1; j < i; j++ {
		if !(s[j] == '_' || isLetter(s[j]) || isDigit(s[j])) {
			return false
		}
	}
	return true
}
