package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	c, err := Parse([]byte(`# shoulder
[keys]
hunk_next = "n"   # next change
hunk_prev = 'p'
top = ["gg", "home",]
quit = []
`))
	if err != nil {
		t.Fatal(err)
	}
	want := []Binding{
		{"hunk_next", []string{"n"}, 3},
		{"hunk_prev", []string{"p"}, 4},
		{"top", []string{"gg", "home"}, 5},
		{"quit", []string{}, 6},
	}
	if !reflect.DeepEqual(c.Keys, want) {
		t.Fatalf("got %+v", c.Keys)
	}
}

func TestParseSettings(t *testing.T) {
	c, err := Parse([]byte(`since = "6h"   # a long day
all = true
signs = false
hide_done = true
editor = "code -w"

[keys]
quit = "x"
`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Since == nil || *c.Since != 6*time.Hour || c.All == nil || !*c.All || c.Signs == nil || *c.Signs ||
		c.Splits != nil || c.HideDone == nil || !*c.HideDone || c.Editor != "code -w" || len(c.Keys) != 1 {
		t.Fatalf("got %+v", c)
	}
}

func TestParseByteOrderMark(t *testing.T) {
	for _, in := range []string{"\ufeff# shoulder\nall = true\n", "\ufeffall = true\r\n"} {
		c, err := Parse([]byte(in))
		if err != nil || c.All == nil || !*c.All {
			t.Errorf("%q: got %+v %v", in, c, err)
		}
	}
}

func TestParseErrors(t *testing.T) {
	cases := map[string]string{
		"hunk_next = \"n\"":              "line 1: unknown setting \"hunk_next\". The settings are since",
		"all = yes":                      "line 1: all: write true or false",
		"since = \"soon\"":               "line 1: since: write a duration",
		"since = 30":                     "line 1: since: put the value in quotes",
		"editor =":                       "line 1: editor: the value is missing",
		"signs =":                        "line 1: signs: write true or false",
		"[colors]":                       "line 1: unknown table [colors]",
		"[keys]\nhunk_next = n":          "line 2: hunk_next: put the value in quotes",
		"[keys]\nhunk_next = \"n":        "line 2: hunk_next: the string has no closing quote",
		"[keys]\na = [\"n\" \"m\"]":      "line 2: a: put a comma",
		"[keys]\na = [\"n\"":             "line 2: a: the array has no closing ]",
		"[keys]\na = \"n\" b":            "line 2: a: there is text after the value",
		"[keys]\na = \"n\"\na = \"m\"":   "line 3: a is already set on line 2",
		"[keys]\nnext key = \"n\"":       "line 2: write a setting",
		"[keys]\na =":                    "line 2: a: the value is missing",
		"[[keys]]":                       "line 1: write a table header",
		"[keys]\na = \"\\q\"":            "line 2: a: the string has an escape",
		"[keys]\na = [\"n\", 'm', \"x\"": "line 2: a: the array has no closing ]",
	}
	for in, want := range cases {
		_, err := Parse([]byte(in))
		if err == nil || !strings.HasPrefix(err.Error(), want) {
			t.Errorf("%q: got %v, want %q", in, err, want)
		}
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SHOULDER_CONFIG", "")
	t.Setenv("XDG_CONFIG_HOME", dir)
	c, err := Load()
	if err != nil || len(c.Keys) != 0 || c.Path != filepath.Join(dir, "shoulder", "config.toml") {
		t.Fatalf("missing default file: %+v %v", c, err)
	}

	p := filepath.Join(dir, "mine.toml")
	t.Setenv("SHOULDER_CONFIG", p)
	if _, err := Load(); err == nil {
		t.Fatal("a missing SHOULDER_CONFIG file gives no error")
	}
	if err := os.WriteFile(p, []byte("[keys]\nquit = 'x'\nbad\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil || !strings.HasPrefix(err.Error(), p+": line 3:") {
		t.Fatalf("got %v", err)
	}
}
