package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bjarneo/shoulder/internal/repo"
	"github.com/bjarneo/shoulder/internal/watch"
)

// statusTail is the part of each log that status reads. A status bar runs
// status every few seconds, and the end of a log gives the state.
const statusTail = 1 << 20

// barStatus is the output of status --json, in the form of a Waybar custom
// module.
type barStatus struct {
	Text    string `json:"text"`
	Tooltip string `json:"tooltip"`
	Class   string `json:"class"`
	Alt     string `json:"alt"`
}

// status prints one line about the agents, for a status bar such as Waybar
// or the tmux status line.
func status(args []string) {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	since := fs.Duration("since", time.Hour, "count sessions with activity in this period")
	all := fs.Bool("all", false, "count the agents of all projects, not only of dir")
	asJSON := fs.Bool("json", false, "print JSON for a Waybar custom module")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: shoulder status [flags] [dir]")
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "Print the number of agents that work (●), wait for you (○) and are done (✓).")
		fmt.Fprintln(os.Stderr, "With no agents, the line is empty.")
		fmt.Fprintln(os.Stderr)
		fs.PrintDefaults()
	}
	rest, _ := parseArgs(fs, args)
	dir := dirArg(rest)
	defaults(flagsSet(fs), loadConfig(), since, all)

	r := repo.Open(dir)
	s := watch.New(watch.Options{Root: r.Root, All: *all, Since: *since, Tail: statusTail}).Once()
	out := summarize(s, r, time.Now())
	if *asJSON {
		_ = json.NewEncoder(os.Stdout).Encode(waybar(out))
		return
	}
	fmt.Println(out.Text)
}

// markup escapes text for Pango markup.
var markup = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#39;")

// waybar returns out with the text and the tooltip escaped for Pango
// markup, because Waybar reads both as markup. Without the escape, a prompt
// such as "a < b" gives an empty tooltip.
func waybar(out barStatus) barStatus {
	out.Text = markup.Replace(out.Text)
	out.Tooltip = markup.Replace(out.Tooltip)
	return out
}

// summarize counts the agents by state and lists them for the tooltip.
func summarize(s watch.Snapshot, r *repo.Repo, now time.Time) barStatus {
	var act, wait, done int
	var tips []string
	for i := range s.Agents {
		a := &s.Agents[i]
		x := a.Activity(now)
		glyph := "●"
		switch x.Status {
		case watch.Active:
			act++
		case watch.Waiting:
			wait++
			glyph = "○"
		default:
			done++
			glyph = "✓"
		}
		project := r.Name
		if a.Cwd != "" && !r.Inside(a.Cwd) {
			project = filepath.Base(a.Cwd)
		}
		tips = append(tips, glyph+" A"+strconv.Itoa(i+1)+" "+a.Name+" · "+project+" · "+x.Act+" · "+a.Task())
	}
	out := barStatus{Class: "none", Tooltip: strings.Join(tips, "\n")}
	switch {
	case wait > 0:
		out.Class = "waiting"
	case act > 0:
		out.Class = "active"
	case done > 0:
		out.Class = "done"
	}
	out.Alt = out.Class
	if len(s.Agents) > 0 {
		out.Text = fmt.Sprintf("●%d ○%d ✓%d", act, wait, done)
	}
	return out
}
