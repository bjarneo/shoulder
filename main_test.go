package main

import (
	"flag"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseArgs(t *testing.T) {
	cases := []struct {
		args  []string
		rest  []string
		all   bool
		since time.Duration
	}{
		{[]string{"~/proj", "--all", "--since", "6h"}, []string{"~/proj"}, true, 6 * time.Hour},
		{[]string{"--since=2h", "~/proj", "-all"}, []string{"~/proj"}, true, 2 * time.Hour},
		{[]string{"a", "b"}, []string{"a", "b"}, false, time.Hour},
		{[]string{"--all", "--", "-dir"}, []string{"-dir"}, true, time.Hour},
		{nil, nil, false, time.Hour},
	}
	for _, c := range cases {
		fs := flag.NewFlagSet("shoulder", flag.ContinueOnError)
		all := fs.Bool("all", false, "")
		since := fs.Duration("since", time.Hour, "")
		rest, err := parseArgs(fs, c.args)
		if err != nil || !reflect.DeepEqual(rest, c.rest) || *all != c.all || *since != c.since {
			t.Errorf("%q: rest %q all %v since %v err %v", c.args, rest, *all, *since, err)
		}
		if set := flagsSet(fs); set["all"] != c.all {
			t.Errorf("%q: flagsSet %v", c.args, set)
		}
	}

	fs := flag.NewFlagSet("shoulder", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if _, err := parseArgs(fs, []string{"dir", "--nope"}); err == nil {
		t.Error("an unknown flag after the dir gives no error")
	}
}

func TestDirOf(t *testing.T) {
	dir := t.TempDir()
	if got, err := dirOf(nil); got != "." || err != nil {
		t.Errorf("no argument: %q %v", got, err)
	}
	if got, err := dirOf([]string{dir}); got != dir || err != nil {
		t.Errorf("one directory: %q %v", got, err)
	}
	if _, err := dirOf([]string{dir + "/missing"}); err == nil || !strings.HasSuffix(err.Error(), "is not a directory") {
		t.Errorf("missing directory: %v", err)
	}
	if _, err := dirOf([]string{dir, dir}); err == nil || !strings.HasPrefix(err.Error(), "give one directory, not 2") {
		t.Errorf("two directories: %v", err)
	}
}
