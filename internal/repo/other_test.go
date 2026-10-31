package repo

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func gitRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"add", "."},
		{"-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-q", "-m", "first"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	return dir
}

func TestFileInOtherRepoUsesItsHead(t *testing.T) {
	a := gitRepo(t, map[string]string{"a.txt": "a\n"})
	b := gitRepo(t, map[string]string{"b.txt": "one\ntwo\n"})
	path := filepath.Join(b, "b.txt")
	if err := os.WriteFile(path, []byte("one\n2\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := Open(a)
	f := r.File(path)
	if f.Mod != 1 || f.Add != 1 || f.Del != 0 || f.Created {
		t.Fatalf("diff against the HEAD of the other repo: mod %d add %d del %d created %v", f.Mod, f.Add, f.Del, f.Created)
	}
}

func TestChangedRange(t *testing.T) {
	cases := []struct {
		a, b     []string
		from, to int
		ok       bool
	}{
		{[]string{"a", "b", "c"}, []string{"a", "b", "c"}, 0, 0, false},
		{[]string{"a", "b", "c"}, []string{"a", "X", "c"}, 1, 1, true},
		{[]string{"a", "b", "c"}, []string{"a", "b", "c", "d", "e"}, 3, 4, true},
		{[]string{"a", "b", "c", "d"}, []string{"a", "d"}, 1, 1, true},
		{[]string{"a"}, []string{}, 0, 0, true},
	}
	for _, c := range cases {
		from, to, ok := changedRange(c.a, c.b)
		if from != c.from || to != c.to || ok != c.ok {
			t.Errorf("%v → %v: got %d %d %v", c.a, c.b, from, to, ok)
		}
	}
}

func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	base := []string{"-C", dir, "-c", "user.name=test", "-c", "user.email=test@example.com", "-c", "protocol.file.allow=always"}
	if out, err := exec.Command("git", append(base, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func keys(m map[string]Change) map[string]byte {
	out := map[string]byte{}
	for p, c := range m {
		out[p] = c.Code
	}
	return out
}

func TestStatusRenameAndCopy(t *testing.T) {
	t.Run("rename in the work tree", func(t *testing.T) {
		dir := gitRepo(t, map[string]string{"src_original.go": "package a\n\nfunc A() {}\n"})
		if err := os.Rename(filepath.Join(dir, "src_original.go"), filepath.Join(dir, "moved.go")); err != nil {
			t.Fatal(err)
		}
		run(t, dir, "add", "-N", "moved.go") // " R moved.go\0src_original.go\0"
		got := keys(Open(dir).Refresh().Changes)
		if want := map[string]byte{"moved.go": 'R'}; !reflect.DeepEqual(got, want) {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
	t.Run("copy with an old path that starts with R", func(t *testing.T) {
		body := "1\n2\n3\n4\n5\n6\n7\n8\n"
		dir := gitRepo(t, map[string]string{"README.md": body, "zeta.go": "package z\n"})
		run(t, dir, "config", "status.renames", "copies")
		write(t, filepath.Join(dir, "README-copy.md"), body)
		write(t, filepath.Join(dir, "README.md"), body+"9\n")
		run(t, dir, "add", ".")
		write(t, filepath.Join(dir, "zeta.go"), "package zeta\n")
		// "C  README-copy.md\0README.md\0M  README.md\0 M zeta.go\0"
		got := keys(Open(dir).Refresh().Changes)
		if want := map[string]byte{"README-copy.md": 'A', "README.md": 'M', "zeta.go": 'M'}; !reflect.DeepEqual(got, want) {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
}

func TestRefreshCountsUntrackedFiles(t *testing.T) {
	dir := gitRepo(t, map[string]string{"a.txt": "a\n"})
	text := filepath.Join(dir, "new.txt")
	write(t, text, "one\ntwo\nthree")
	write(t, filepath.Join(dir, "image.bin"), "PNG\x00\x01\n\n\n")
	big, err := os.Create(filepath.Join(dir, "dump.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if err := big.Truncate(maxSize + 1); err != nil {
		t.Fatal(err)
	}
	big.Close()

	r := Open(dir)
	var m0, m1 runtime.MemStats
	runtime.ReadMemStats(&m0)
	st := r.Refresh()
	runtime.ReadMemStats(&m1)
	if got := m1.TotalAlloc - m0.TotalAlloc; got > maxSize/2 {
		t.Errorf("Refresh allocated %d bytes, so it read the file over maxSize", got)
	}
	if c := st.Changes["new.txt"]; c.Code != 'A' || c.Add != 3 {
		t.Errorf("text file: %+v", c)
	}
	if c := st.Changes["image.bin"]; c.Add != 0 {
		t.Errorf("binary file: %+v", c)
	}
	if c := st.Changes["dump.sql"]; c.Add != 0 {
		t.Errorf("file over maxSize: %+v", c)
	}

	// The same modification time and size use the cached count.
	info, _ := os.Stat(text)
	write(t, text, "1\n2\n3\n4\n5\n6\n7")
	if err := os.Truncate(text, info.Size()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(text, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if c := r.Refresh().Changes["new.txt"]; c.Add != 3 {
		t.Errorf("cached count: got %d, want 3", c.Add)
	}
	// A new modification time counts the lines again.
	later := info.ModTime().Add(time.Second)
	if err := os.Chtimes(text, later, later); err != nil {
		t.Fatal(err)
	}
	if c := r.Refresh().Changes["new.txt"]; c.Add != 7 {
		t.Errorf("count after a change: got %d, want 7", c.Add)
	}
}

func TestFileComesBack(t *testing.T) {
	dir := gitRepo(t, map[string]string{"a.txt": "one\ntwo\n"})
	p := filepath.Join(dir, "a.txt")
	write(t, p, "one\nTWO\n")
	r := Open(dir)
	r.Apply(r.Refresh())
	r.File(p)
	away := filepath.Join(dir, "a.txt.bak")
	if err := os.Rename(p, away); err != nil {
		t.Fatal(err)
	}
	time.Sleep(statEvery + 20*time.Millisecond)
	if !r.File(p).Missing {
		t.Fatal("the file is not missing after mv")
	}
	// mv keeps the modification time and the size.
	if err := os.Rename(away, p); err != nil {
		t.Fatal(err)
	}
	time.Sleep(statEvery + 20*time.Millisecond)
	if f := r.File(p); f.Missing || f.Mod != 1 {
		t.Fatalf("after mv back: missing %v mod %d", f.Missing, f.Mod)
	}
}

func TestFileReadErrorClearsLines(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root can read a file with mode 000")
	}
	dir := gitRepo(t, map[string]string{"a.txt": "one\n"})
	p := filepath.Join(dir, "a.txt")
	write(t, p, "one\ntwo\n")
	r := Open(dir)
	r.File(p)
	if err := os.Chmod(p, 0); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(p, 0o644)
	later := time.Now().Add(time.Second)
	if err := os.Chtimes(p, later, later); err != nil {
		t.Fatal(err)
	}
	time.Sleep(statEvery + 20*time.Millisecond)
	if f := r.File(p); !f.Missing || len(f.Lines) != 0 {
		t.Fatalf("unreadable file: missing %v, %d lines", f.Missing, len(f.Lines))
	}
}

func TestOtherRepoCommitDropsBaseline(t *testing.T) {
	a := gitRepo(t, map[string]string{"a.txt": "a\n"})
	b := gitRepo(t, map[string]string{"b.txt": "one\ntwo\n"})
	path := filepath.Join(b, "b.txt")
	write(t, path, "one\n2\n")
	r := Open(a)
	r.Apply(r.Refresh())
	if f := r.File(path); f.Mod != 1 {
		t.Fatalf("before the commit: mod %d", f.Mod)
	}
	r.Apply(r.Refresh())
	run(t, b, "commit", "-qam", "second")
	done := make(chan State)
	go func() { done <- r.Refresh() }() // Refresh runs in another goroutine in the UI.
	r.File(path)
	r.Apply(<-done)
	time.Sleep(statEvery + 20*time.Millisecond)
	if f := r.File(path); f.Changed() {
		t.Fatalf("after the commit in the other repository: mod %d add %d del %d", f.Mod, f.Add, f.Del)
	}
}

func TestSubmoduleAndNestedRepo(t *testing.T) {
	sub := gitRepo(t, map[string]string{"lib.go": "package lib\n\nfunc A() {}\n"})
	top := gitRepo(t, map[string]string{"main.go": "package main\n"})
	run(t, top, "submodule", "add", "-q", sub, "vendor/lib")
	run(t, top, "commit", "-qm", "add the submodule")
	inSub := filepath.Join(top, "vendor", "lib", "lib.go")
	write(t, inSub, "package lib\n\nfunc A() { _ = 1 }\n")

	nested := filepath.Join(top, "tools")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, nested, "init", "-q")
	inNested := filepath.Join(nested, "gen", "gen.go")
	write(t, inNested, "package gen\n")
	run(t, nested, "add", ".")
	run(t, nested, "commit", "-qm", "first")
	write(t, inNested, "package gen\n\nvar X = 1\n")

	r := Open(top)
	st := r.Refresh()
	r.Apply(st)
	for p := range st.Changes {
		if p == "vendor/lib" || p == "tools/" || p == "tools" {
			t.Errorf("the repository directory %q is in the changes", p)
		}
	}
	if f := r.File(inSub); f.Created || f.Mod != 1 || f.Add != 0 {
		t.Errorf("submodule file: created %v mod %d add %d", f.Created, f.Mod, f.Add)
	}
	if f := r.File(inNested); f.Created || f.Add != 2 {
		t.Errorf("nested repository file: created %v add %d", f.Created, f.Add)
	}
}

func TestLineEndings(t *testing.T) {
	t.Run("LF to CRLF is a change", func(t *testing.T) {
		dir := gitRepo(t, map[string]string{"a.txt": "one\ntwo\n"})
		p := filepath.Join(dir, "a.txt")
		write(t, p, "one\r\ntwo\r\n")
		f := Open(dir).File(p)
		if f.Mod != 2 {
			t.Fatalf("mod %d, want 2", f.Mod)
		}
		for _, l := range f.Lines {
			if strings.ContainsRune(l.Text, '\r') || strings.ContainsRune(l.Old, '\r') {
				t.Fatalf("the line shows the CR: %q", l.Text)
			}
		}
	})
	t.Run("eol=crlf in .gitattributes is not a change", func(t *testing.T) {
		dir := gitRepo(t, map[string]string{".gitattributes": "*.txt text eol=crlf\n", "a.txt": "one\ntwo\n"})
		p := filepath.Join(dir, "a.txt")
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
		run(t, dir, "checkout", "--", "a.txt")
		if b, _ := os.ReadFile(p); string(b) != "one\r\ntwo\r\n" {
			t.Fatalf("checkout wrote %q", b)
		}
		if f := Open(dir).File(p); f.Changed() {
			t.Fatalf("mod %d add %d del %d", f.Mod, f.Add, f.Del)
		}
	})
}
