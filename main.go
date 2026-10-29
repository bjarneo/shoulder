// shoulder shows what coding agents do in a project, live: the files they
// read and edit, the commands they run, and the diff against the last commit.
// It reads the session logs of Claude Code, Codex, Gemini CLI and opencode.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"runtime/debug"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/bjarneo/shoulder/internal/config"
	"github.com/bjarneo/shoulder/internal/repo"
	"github.com/bjarneo/shoulder/internal/ui"
	"github.com/bjarneo/shoulder/internal/watch"
)

// version is set at build time with -ldflags "-X main.version=v0.1.0".
var version = "dev"

func main() {
	since := flag.Duration("since", time.Hour, "show sessions with activity in this period")
	splits := flag.Bool("splits", false, "start with one split per agent")
	signs := flag.Bool("signs", false, "show changes as signs instead of inline deleted lines")
	all := flag.Bool("all", false, "watch the agents of all projects, not only of dir")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: shoulder [flags] [dir]")
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "Watch the coding agents that work in dir, or in the current directory.")
		fmt.Fprintln(os.Stderr, "shoulder reads the sessions of Claude Code, Codex, Gemini CLI, opencode and omp.")
		fmt.Fprintln(os.Stderr)
		flag.PrintDefaults()
		p, _ := config.Path()
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "To change the keys, edit "+p+", or the file in SHOULDER_CONFIG.")
	}
	flag.Parse()
	if *showVersion {
		// go install builds carry the module version instead of ldflags.
		if info, ok := debug.ReadBuildInfo(); ok && version == "dev" && info.Main.Version != "(devel)" && info.Main.Version != "" {
			version = info.Main.Version
		}
		fmt.Println("shoulder", version)
		return
	}

	dir := "."
	if flag.NArg() > 0 {
		dir = flag.Arg(0)
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		fmt.Fprintf(os.Stderr, "shoulder: %s is not a directory\n", dir)
		os.Exit(2)
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "shoulder:", err)
		os.Exit(2)
	}
	keys, err := ui.NewKeymap(cfg.Keys)
	if err != nil {
		fmt.Fprintf(os.Stderr, "shoulder: %s: %v\n", cfg.Path, err)
		os.Exit(2)
	}

	r := repo.Open(dir)
	w := watch.New(watch.Options{Root: r.Root, All: *all, Since: *since})
	m := ui.New(r, w, ui.Options{Splits: *splits, Signs: *signs, All: *all, Since: *since, Keys: keys})
	p := tea.NewProgram(m)

	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx, func(s watch.Snapshot) { p.Send(ui.Snapshot(s)) })
	_, err = p.Run()
	cancel()
	if err != nil {
		fmt.Fprintln(os.Stderr, "shoulder:", err)
		os.Exit(1)
	}
}
