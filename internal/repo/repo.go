// Package repo reads the project files and compares them with the last
// commit, so that the file view can show what the agents changed.
package repo

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Repo is the project that shoulder watches.
type Repo struct {
	Root string
	Name string
	Git  bool

	State State
	files map[string]*File
	// first holds the first content seen of each file outside git, as the
	// baseline for the diff.
	first map[string][]string
	tops  map[string]string // the git top level of each directory outside Root
}

// State is the git state of the project. Refresh reads it.
type State struct {
	Branch  string
	Head    string
	Changes map[string]Change // by path relative to Root
}

// Change is the git status of one file.
type Change struct {
	Code byte // 'M' modified, 'A' added or untracked, 'D' deleted, 'R' renamed
	Add  int
	Del  int
}

// Open finds the git top level of dir. A directory outside git also works,
// without a baseline from the last commit.
func Open(dir string) *Repo {
	dir, _ = filepath.Abs(dir)
	r := &Repo{Root: dir, files: map[string]*File{}, first: map[string][]string{}, tops: map[string]string{}}
	if out, err := git(dir, "rev-parse", "--show-toplevel"); err == nil {
		r.Root = strings.TrimSpace(out)
		r.Git = true
	}
	r.Name = filepath.Base(r.Root)
	return r
}

func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir, "--no-optional-locks"}, args...)...)
	out, err := cmd.Output()
	return string(out), err
}

// Refresh reads the git state. It is safe to call from another goroutine,
// because it does not change r.
func (r *Repo) Refresh() State {
	st := State{Changes: map[string]Change{}}
	if !r.Git {
		return st
	}
	if out, err := git(r.Root, "branch", "--show-current"); err == nil {
		st.Branch = strings.TrimSpace(out)
	}
	if out, err := git(r.Root, "rev-parse", "HEAD"); err == nil {
		st.Head = strings.TrimSpace(out)
	}
	if st.Branch == "" && len(st.Head) >= 7 {
		st.Branch = st.Head[:7]
	}
	out, err := git(r.Root, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return st
	}
	fields := strings.Split(out, "\x00")
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		if len(f) < 4 {
			continue
		}
		x, y, path := f[0], f[1], f[3:]
		c := Change{Code: 'M'}
		switch {
		case x == '?' || x == 'A':
			c.Code = 'A'
		case x == 'D' || y == 'D':
			c.Code = 'D'
		case x == 'R':
			c.Code = 'R'
			i++ // the next field is the old path
		}
		st.Changes[path] = c
	}
	if out, err := git(r.Root, "diff", "HEAD", "--numstat", "-z", "--no-renames"); err == nil {
		for _, rec := range strings.Split(out, "\x00") {
			parts := strings.SplitN(rec, "\t", 3)
			if len(parts) != 3 {
				continue
			}
			c, ok := st.Changes[parts[2]]
			if !ok {
				continue
			}
			c.Add, _ = strconv.Atoi(parts[0])
			c.Del, _ = strconv.Atoi(parts[1])
			st.Changes[parts[2]] = c
		}
	}
	for p, c := range st.Changes {
		if c.Code == 'A' && c.Add == 0 {
			if b, err := os.ReadFile(filepath.Join(r.Root, p)); err == nil && len(b) < maxSize {
				c.Add = len(splitLines(b))
				st.Changes[p] = c
			}
		}
	}
	return st
}

// Apply stores a state from Refresh. A new HEAD drops the cached baselines.
func (r *Repo) Apply(st State) {
	if st.Head != r.State.Head {
		for _, f := range r.files {
			f.baseOK = false
			f.stamp = time.Time{}
		}
	}
	r.State = st
}

// topOf returns the git top level of a directory, or "" outside git.
func (r *Repo) topOf(dir string) string {
	if t, ok := r.tops[dir]; ok {
		return t
	}
	t := ""
	if out, err := git(dir, "rev-parse", "--show-toplevel"); err == nil {
		t = strings.TrimSpace(out)
	}
	r.tops[dir] = t
	return t
}

// Rel returns path relative to the root, or the path itself when it is
// outside the root.
func (r *Repo) Rel(path string) string {
	if rel, err := filepath.Rel(r.Root, path); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return path
}

// Inside reports whether path is in the project.
func (r *Repo) Inside(path string) bool {
	return path == r.Root || strings.HasPrefix(path, r.Root+string(filepath.Separator))
}

const (
	maxSize   = 4 << 20
	statEvery = 300 * time.Millisecond
)

// LineState is the diff state of one line of the current file.
type LineState uint8

const (
	Same LineState = iota
	Added
	Changed
)

// Line is one line of the current file with its diff state.
type Line struct {
	Text  string
	State LineState
	Old   string   // the replaced text of a Changed line
	Above []string // deleted lines just above this line
	Below []string // deleted lines just below this line, at the end of the file
}

// File is a loaded project file.
type File struct {
	Path    string
	Lines   []Line
	Hunks   []int // first line of each change
	Add     int
	Mod     int
	Del     int
	Created bool
	Missing bool
	Binary  bool
	TooBig  bool
	Version int // grows on each change of content

	stamp   time.Time
	mtime   time.Time
	size    int64
	base    []string
	baseOK  bool
	hasBase bool
	raw     []string
	pending []ghost
}

// File returns the file at path. It reloads the file when it changed on
// disk, at most once per statEvery.
func (r *Repo) File(path string) *File {
	f := r.files[path]
	if f == nil {
		f = &File{Path: path}
		r.files[path] = f
	}
	if time.Since(f.stamp) < statEvery {
		return f
	}
	f.stamp = time.Now()
	st, err := os.Stat(path)
	baseStale := !f.baseOK
	if !baseStale && err == nil && st.ModTime().Equal(f.mtime) && st.Size() == f.size {
		return f
	}
	f.Missing, f.Binary, f.TooBig = false, false, false
	switch {
	case err != nil || st.IsDir():
		f.Missing = true
		f.raw = nil
	case st.Size() > maxSize:
		f.TooBig = true
		f.raw = nil
	default:
		b, err := os.ReadFile(path)
		if err != nil {
			f.Missing = true
			break
		}
		if bytes.IndexByte(b[:min(len(b), 8000)], 0) >= 0 {
			f.Binary = true
			f.raw = nil
			break
		}
		f.raw = splitLines(b)
	}
	if err == nil {
		f.mtime, f.size = st.ModTime(), st.Size()
	}
	if baseStale {
		f.base, f.hasBase = r.baseline(path, f)
		f.baseOK = true
	}
	f.diff()
	f.Version++
	return f
}

// baseline returns the old content of a file: the version in HEAD, or the
// first content that shoulder saw for a file outside git. It reports false
// for a file that has no old version. A file in another project uses the
// HEAD of its own repository.
func (r *Repo) baseline(path string, f *File) ([]string, bool) {
	top := ""
	switch {
	case r.Inside(path):
		if r.Git {
			top = r.Root
		}
	default:
		top = r.topOf(filepath.Dir(path))
	}
	if top != "" {
		rel, _ := filepath.Rel(top, path)
		out, err := git(top, "show", "HEAD:"+filepath.ToSlash(rel))
		if err != nil {
			return nil, false
		}
		return splitLines([]byte(out)), true
	}
	if b, ok := r.first[path]; ok {
		return b, true
	}
	r.first[path] = f.raw
	return f.raw, f.raw != nil
}

// diff compares the current lines with the baseline.
func (f *File) diff() {
	f.Lines = f.Lines[:0]
	f.Hunks, f.Add, f.Mod, f.Del = nil, 0, 0, 0
	f.Created = !f.hasBase && !f.Missing
	if !f.hasBase {
		for _, t := range f.raw {
			f.Lines = append(f.Lines, Line{Text: t, State: Added})
		}
		f.Add = len(f.raw)
		f.hunks()
		return
	}
	ops := diffLines(f.base, f.raw)
	if ops == nil {
		f.roughDiff()
		f.hunks()
		return
	}
	var dels []string
	var ins []int
	flush := func() {
		pairs := min(len(dels), len(ins))
		for i := 0; i < pairs; i++ {
			l := &f.Lines[ins[i]]
			l.State, l.Old = Changed, dels[i]
			f.Mod++
		}
		for _, i := range ins[pairs:] {
			f.Lines[i].State = Added
			f.Add++
		}
		if rest := dels[pairs:]; len(rest) > 0 {
			f.Del += len(rest)
			// Show deleted lines above the next line, or below the last line
			// at the end of the file.
			next := len(f.Lines)
			if len(ins) > 0 {
				next = ins[len(ins)-1] + 1
			}
			f.pending = append(f.pending, ghost{at: next, text: append([]string(nil), rest...)})
		}
		dels, ins = dels[:0], ins[:0]
	}
	f.pending = f.pending[:0]
	for _, o := range ops {
		switch o.kind {
		case opEq:
			flush()
			f.Lines = append(f.Lines, Line{Text: f.raw[o.b]})
		case opDel:
			dels = append(dels, f.base[o.a])
		case opIns:
			ins = append(ins, len(f.Lines))
			f.Lines = append(f.Lines, Line{Text: f.raw[o.b]})
		}
	}
	flush()
	for _, g := range f.pending {
		switch {
		case g.at < len(f.Lines):
			f.Lines[g.at].Above = append(f.Lines[g.at].Above, g.text...)
		case len(f.Lines) > 0:
			last := &f.Lines[len(f.Lines)-1]
			last.Below = append(last.Below, g.text...)
		}
	}
	f.hunks()
}

// roughDiff marks lines that do not occur in the baseline. It is the
// fallback for files with too many changes for the exact diff.
func (f *File) roughDiff() {
	count := map[string]int{}
	for _, l := range f.base {
		count[l]++
	}
	for _, t := range f.raw {
		l := Line{Text: t}
		if count[t] > 0 {
			count[t]--
		} else {
			l.State = Added
			f.Add++
		}
		f.Lines = append(f.Lines, l)
	}
	for _, n := range count {
		f.Del += n
	}
}

func (f *File) hunks() {
	changed := func(i int) bool {
		if i < 0 || i >= len(f.Lines) {
			return false
		}
		l := &f.Lines[i]
		return l.State != Same || len(l.Above) > 0 || len(l.Below) > 0
	}
	for i := range f.Lines {
		if changed(i) && !changed(i-1) {
			f.Hunks = append(f.Hunks, i)
		}
	}
}

// Changed reports whether the file differs from its baseline.
func (f *File) Changed() bool { return f.Add+f.Mod+f.Del > 0 }

type ghost struct {
	at   int
	text []string
}

func splitLines(b []byte) []string {
	if len(b) == 0 {
		return nil
	}
	s := strings.ReplaceAll(string(b), "\r\n", "\n")
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}
