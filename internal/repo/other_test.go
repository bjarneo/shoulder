package repo

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
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
