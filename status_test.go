package main

import (
	"testing"
	"time"

	"github.com/bjarneo/shoulder/internal/repo"
	"github.com/bjarneo/shoulder/internal/watch"
)

func TestSummarize(t *testing.T) {
	dir := t.TempDir()
	r := repo.Open(dir)
	now := time.Now()
	s := watch.Snapshot{Agents: []watch.Agent{
		{Name: "claude", Cwd: dir, Prompt: "fix the bug", Updated: now,
			Events: []watch.Event{{Time: now, Kind: watch.KindRun, Cmd: "go test", Result: watch.Pending}}},
		{Name: "codex", Cwd: "/w/other", Prompt: "add tests", Updated: now,
			Events: []watch.Event{{Time: now, Kind: watch.KindAsk, Text: "Which file?", Result: watch.Pending}}},
		{Name: "gemini", Cwd: dir, Prompt: "write docs", Updated: now, TurnEnded: true,
			Events: []watch.Event{{Time: now, Kind: watch.KindDone}}},
	}}
	got := summarize(s, r, now)
	want := barStatus{
		Text:    "●1 ○1 ✓1",
		Tooltip: "● A1 claude · " + r.Name + " · running · fix the bug\n○ A2 codex · other · waiting · add tests\n✓ A3 gemini · " + r.Name + " · done · write docs",
		Class:   "waiting", Alt: "waiting",
	}
	if got != want {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
	if empty := summarize(watch.Snapshot{}, r, now); empty.Text != "" || empty.Class != "none" {
		t.Fatalf("no agents: %+v", empty)
	}
}

func TestWaybarEscapesMarkup(t *testing.T) {
	in := barStatus{Text: "●1 ○0 ✓0", Tooltip: `● A1 claude · p · running · fix Vec<T> & "a" > 'b'`, Class: "active", Alt: "active"}
	want := barStatus{Text: "●1 ○0 ✓0", Tooltip: "● A1 claude · p · running · fix Vec&lt;T&gt; &amp; &quot;a&quot; &gt; &#39;b&#39;", Class: "active", Alt: "active"}
	if got := waybar(in); got != want {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}
