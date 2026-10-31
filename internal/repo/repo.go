// Package repo reads the project files and compares them with the last
// commit, so that the file view can show what the agents changed.
package repo

import (
	"bytes"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
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
	sh    *shared
}

// shared holds the data that Refresh uses from another goroutine. The lock
// keeps it safe.
type shared struct {
	mu sync.Mutex
	// others holds the top level of each other repository that has a file
	// with a baseline. Refresh reads the HEAD of each one.
	others map[string]bool
	// counts holds the line counts of untracked files from the last Refresh.
	// Refresh replaces the map and does not change it.
	counts map[string]lineCount
}

// lineCount is the number of lines of an untracked file at a modification
// time and a size.
type lineCount struct {
	mtime time.Time
	size  int64
	n     int
}

// State is the git state of the project. Refresh reads it.
type State struct {
	Branch  string
	Head    string
	Changes map[string]Change // by path relative to Root

	heads map[string]string // the HEAD of each other repository, by top level
}

// Change is the git status of one file.
type Change struct {
	Code byte // 'M' modified, 'A' added, copied or untracked, 'D' deleted, 'R' renamed
	Add  int
	Del  int
}

// Open finds the git top level of dir. A directory outside git also works,
// without a baseline from the last commit.
func Open(dir string) *Repo {
	dir, _ = filepath.Abs(dir)
	r := &Repo{
		Root: dir, files: map[string]*File{}, first: map[string][]string{}, tops: map[string]string{},
		sh: &shared{others: map[string]bool{}},
	}
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
// because it does not change the fields of r. It uses the shared data only
// under its lock.
func (r *Repo) Refresh() State {
	st := State{Changes: map[string]Change{}, heads: r.otherHeads()}
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
	var entries []entry
	fields := strings.Split(out, "\x00")
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		if len(f) < 4 {
			continue
		}
		x, y, path := f[0], f[1], f[3:]
		// A rename or a copy, in the index or in the work tree, has the old
		// path in the next field.
		if x == 'R' || x == 'C' || y == 'R' || y == 'C' {
			i++
		}
		// git shows an untracked nested repository as a directory with a
		// slash. It is not a changed file.
		if strings.HasSuffix(path, "/") {
			continue
		}
		c := Change{Code: 'M'}
		switch {
		case x == '?' || x == 'A':
			c.Code = 'A'
		case x == 'D' || y == 'D':
			c.Code = 'D'
		case x == 'R' || y == 'R':
			c.Code = 'R'
		case x == 'C' || y == 'C':
			c.Code = 'A' // a copy is a new file
		}
		entries = append(entries, entry{path: path, c: c})
	}
	statAll(r.Root, entries)
	added := map[string]fs.FileInfo{}
	for _, e := range entries {
		// A submodule is a directory, not a changed file.
		if e.info != nil && e.info.IsDir() {
			continue
		}
		if e.info != nil && e.c.Code == 'A' {
			added[e.path] = e.info
		}
		st.Changes[e.path] = e.c
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
	r.countLines(st.Changes, added)
	return st
}

// entry is one file of the git status.
type entry struct {
	path string
	c    Change
	info fs.FileInfo // nil for a deleted file or a file that Stat cannot read
}

// statAll reads the file info of each entry that is not deleted. A large
// status has many files, so it uses up to 8 goroutines.
func statAll(root string, es []entry) {
	n := len(es)
	workers := min(runtime.NumCPU(), 8, n/512+1)
	var wg sync.WaitGroup
	for w := range workers {
		lo, hi := n*w/workers, n*(w+1)/workers
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := lo; i < hi; i++ {
				if es[i].c.Code == 'D' {
					continue
				}
				if info, err := os.Stat(filepath.Join(root, es[i].path)); err == nil {
					es[i].info = info
				}
			}
		}()
	}
	wg.Wait()
}

// otherHeads returns the HEAD of each other repository that has a file with
// a baseline, by top level.
func (r *Repo) otherHeads() map[string]string {
	r.sh.mu.Lock()
	tops := make([]string, 0, len(r.sh.others))
	for t := range r.sh.others {
		tops = append(tops, t)
	}
	r.sh.mu.Unlock()
	heads := make(map[string]string, len(tops))
	for _, t := range tops {
		if out, err := git(t, "rev-parse", "HEAD"); err == nil {
			heads[t] = strings.TrimSpace(out)
		}
	}
	return heads
}

// countLines sets the line count of each added file that git diff does not
// count, such as an untracked file. It reads a file only when the
// modification time or the size changed since the last Refresh.
func (r *Repo) countLines(changes map[string]Change, added map[string]fs.FileInfo) {
	r.sh.mu.Lock()
	old := r.sh.counts
	r.sh.mu.Unlock()
	next := make(map[string]lineCount, len(added))
	for p, info := range added {
		c := changes[p]
		if c.Add > 0 {
			continue
		}
		lc, ok := old[p]
		if !ok || !lc.mtime.Equal(info.ModTime()) || lc.size != info.Size() {
			n, ok := fileLines(filepath.Join(r.Root, p), info.Size())
			if !ok {
				continue
			}
			lc = lineCount{mtime: info.ModTime(), size: info.Size(), n: n}
		}
		next[p] = lc
		c.Add = lc.n
		changes[p] = c
	}
	r.sh.mu.Lock()
	r.sh.counts = next
	r.sh.mu.Unlock()
}

// fileLines returns the number of lines of a file. It gives 0 for a binary
// file and for a file over maxSize, without a full read. It reports false
// when it cannot read the file.
func fileLines(path string, size int64) (int, bool) {
	if size == 0 || size > maxSize {
		return 0, true
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	if isBinary(b) {
		return 0, true
	}
	n := bytes.Count(b, []byte{'\n'})
	if len(b) > 0 && b[len(b)-1] != '\n' {
		n++
	}
	return n, true
}

// isBinary reports whether b has a NUL byte in the first 8000 bytes, as git
// does.
func isBinary(b []byte) bool {
	return bytes.IndexByte(b[:min(len(b), 8000)], 0) >= 0
}

// Apply stores a state from Refresh. A new HEAD of the project drops all
// cached baselines. A new HEAD of another repository drops the cached
// baselines of the files in that repository.
func (r *Repo) Apply(st State) {
	newHead := st.Head != r.State.Head
	for _, f := range r.files {
		other := f.top != "" && f.top != r.Root && st.heads[f.top] != r.State.heads[f.top]
		if newHead || other {
			f.baseOK = false
			f.stamp = time.Time{}
		}
	}
	r.State = st
}

// repoTop returns the git top level of the repository that holds path, or
// "" outside git. In the project, a .git between the file and Root marks
// the top level of a submodule or a nested repository.
func (r *Repo) repoTop(path string) string {
	if !r.Inside(path) {
		return r.topOf(filepath.Dir(path))
	}
	for d := filepath.Dir(path); d != r.Root && r.Inside(d); d = filepath.Dir(d) {
		if _, err := os.Lstat(filepath.Join(d, ".git")); err == nil {
			return d
		}
	}
	if r.Git {
		return r.Root
	}
	return ""
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
	Text  string // without the CR of a CRLF line ending
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

	// LastFrom and LastTo are the lines of the last change on disk that
	// shoulder saw, at LastAt.
	LastFrom, LastTo int
	LastAt           time.Time

	stamp   time.Time
	mtime   time.Time
	size    int64
	top     string // the git top level of the baseline, or "" outside git
	base    []string
	baseOK  bool
	hasBase bool
	raw     []string // the lines with a CR at the end of a CRLF line
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
	gone := err != nil || st.IsDir()
	baseStale := !f.baseOK
	if !baseStale {
		switch {
		case gone && f.Missing:
			// The file is still missing.
			return f
		case !gone && !f.Missing && st.ModTime().Equal(f.mtime) && st.Size() == f.size:
			// The file did not change. The check of f.Missing reloads a file
			// that comes back with the same modification time and size, for
			// example after mv.
			return f
		}
	}
	f.Missing, f.Binary, f.TooBig = false, false, false
	switch {
	case gone:
		f.Missing = true
		f.raw = nil
	case st.Size() > maxSize:
		f.TooBig = true
		f.raw = nil
	default:
		b, err := os.ReadFile(path)
		if err != nil {
			f.Missing = true
			f.raw = nil
			break
		}
		if isBinary(b) {
			f.Binary = true
			f.raw = nil
			break
		}
		old := f.raw
		f.raw = splitLines(b)
		if from, to, ok := changedRange(old, f.raw); ok && old != nil {
			f.LastFrom, f.LastTo, f.LastAt = from, to, time.Now()
		}
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

// ModTime is the modification time of the file at the last load.
func (f *File) ModTime() time.Time { return f.mtime }

// changedRange returns the first and the last line of b that differ from a.
// It reports false when a and b are the same.
func changedRange(a, b []string) (int, int, bool) {
	n := min(len(a), len(b))
	i := 0
	for i < n && a[i] == b[i] {
		i++
	}
	if i == len(a) && i == len(b) {
		return 0, 0, false
	}
	j := 0
	for j < n-i && a[len(a)-1-j] == b[len(b)-1-j] {
		j++
	}
	if len(b) == 0 {
		return 0, 0, true
	}
	from := min(i, len(b)-1)
	return from, max(from, len(b)-1-j), true
}

// baseline returns the old content of a file: the version in HEAD, or the
// first content that shoulder saw for a file outside git. It reports false
// for a file that has no old version. A file in another repository, such as
// a submodule or another project, uses the HEAD of its own repository.
func (r *Repo) baseline(path string, f *File) ([]string, bool) {
	top := r.repoTop(path)
	f.top = top
	if top != "" {
		if top != r.Root {
			r.sh.mu.Lock()
			r.sh.others[top] = true
			r.sh.mu.Unlock()
		}
		rel, _ := filepath.Rel(top, path)
		// --filters gives the content in the form of the work tree, with the
		// end-of-line conversion of the repository. Git LFS gives the pointer
		// and does not download the file.
		cmd := exec.Command("git", "-C", top, "--no-optional-locks", "cat-file", "--filters", "HEAD:"+filepath.ToSlash(rel))
		cmd.Env = append(os.Environ(), "GIT_LFS_SKIP_SMUDGE=1")
		out, err := cmd.Output()
		if err != nil {
			return nil, false
		}
		return splitLines(out), true
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
			f.Lines = append(f.Lines, Line{Text: noCR(t), State: Added})
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
			l.State, l.Old = Changed, noCR(dels[i])
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
			text := make([]string, len(rest))
			for i, t := range rest {
				text[i] = noCR(t)
			}
			f.pending = append(f.pending, ghost{at: next, text: text})
		}
		dels, ins = dels[:0], ins[:0]
	}
	f.pending = f.pending[:0]
	for _, o := range ops {
		switch o.kind {
		case opEq:
			flush()
			f.Lines = append(f.Lines, Line{Text: noCR(f.raw[o.b])})
		case opDel:
			dels = append(dels, f.base[o.a])
		case opIns:
			ins = append(ins, len(f.Lines))
			f.Lines = append(f.Lines, Line{Text: noCR(f.raw[o.b])})
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
		l := Line{Text: noCR(t)}
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

// splitLines splits b at each LF. A line of a CRLF file keeps its CR, so
// that a change of the line endings is a change of the lines.
func splitLines(b []byte) []string {
	if len(b) == 0 {
		return nil
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
}

// noCR returns a line without the CR of a CRLF line ending, for the view.
func noCR(s string) string { return strings.TrimSuffix(s, "\r") }
