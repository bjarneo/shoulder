// shoulder shows what coding agents do in a project, live: the files they
// read and edit, the commands they run, and the diff against the last commit.
// It reads the session logs of Claude Code, Codex, Gemini CLI, opencode and
// omp.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/bjarneo/shoulder/internal/config"
	"github.com/bjarneo/shoulder/internal/repo"
	"github.com/bjarneo/shoulder/internal/ui"
	"github.com/bjarneo/shoulder/internal/watch"
)

// version is set at build time with -ldflags "-X main.version=v0.2.0".
var version = "dev"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "status" {
		status(os.Args[2:])
		return
	}
	since := flag.Duration("since", time.Hour, "show sessions with activity in this period")
	splits := flag.Bool("splits", false, "start with one split per agent")
	signs := flag.Bool("signs", false, "show changes as signs instead of inline deleted lines")
	all := flag.Bool("all", false, "watch the agents of all projects, not only of dir")
	hideDone := flag.Bool("hide-done", false, "hide the agents that are done")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: shoulder [flags] [dir]")
		fmt.Fprintln(os.Stderr, "       shoulder status [flags] [dir]")
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "Watch the coding agents that work in dir, or in the current directory.")
		fmt.Fprintln(os.Stderr, "shoulder reads the sessions of Claude Code, Codex, Gemini CLI, opencode and omp.")
		fmt.Fprintln(os.Stderr, "shoulder status prints one line about the agents, for a status bar.")
		fmt.Fprintln(os.Stderr)
		flag.PrintDefaults()
		p, _ := config.Path()
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "To change the defaults and the keys, edit "+p+", or the file in SHOULDER_CONFIG.")
	}
	args, _ := parseArgs(flag.CommandLine, os.Args[1:])
	if *showVersion {
		// go install builds carry the module version instead of ldflags.
		if info, ok := debug.ReadBuildInfo(); ok && version == "dev" && info.Main.Version != "(devel)" && info.Main.Version != "" {
			version = info.Main.Version
		}
		fmt.Println("shoulder", version)
		return
	}

	dir := dirArg(args)
	cfg := loadConfig()
	set := flagsSet(flag.CommandLine)
	defaults(set, cfg, since, all)
	if cfg.Splits != nil && !set["splits"] {
		*splits = *cfg.Splits
	}
	if cfg.Signs != nil && !set["signs"] {
		*signs = *cfg.Signs
	}
	if cfg.HideDone != nil && !set["hide-done"] {
		*hideDone = *cfg.HideDone
	}
	keys, err := ui.NewKeymap(cfg.Keys)
	if err != nil {
		fmt.Fprintf(os.Stderr, "shoulder: %s: %v\n", cfg.Path, err)
		os.Exit(2)
	}

	r := repo.Open(dir)
	w := watch.New(watch.Options{Root: r.Root, All: *all, Since: *since})
	m := ui.New(r, w, ui.Options{
		Splits: *splits, Signs: *signs, All: *all, HideDone: *hideDone, Since: *since,
		Keys: keys, Editor: editor(cfg),
	})
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

// parseArgs parses the flags in args, also the flags after the dir
// argument, such as "shoulder ~/proj --all". It returns the arguments that
// are not flags. The arguments after "--" are never flags.
func parseArgs(fs *flag.FlagSet, args []string) ([]string, error) {
	var rest []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		left := fs.Args()
		if used := len(args) - len(left); used > 0 && args[used-1] == "--" {
			return append(rest, left...), nil
		}
		if len(left) == 0 {
			return rest, nil
		}
		rest = append(rest, left[0])
		args = left[1:]
	}
}

// dirArg returns the directory argument, or ".". It stops the program when
// the argument is not a directory, or when there is more than one argument.
func dirArg(args []string) string {
	dir, err := dirOf(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "shoulder:", err)
		os.Exit(2)
	}
	return dir
}

// dirOf returns the directory argument, or "." when there is none.
func dirOf(args []string) (string, error) {
	switch len(args) {
	case 0:
		return ".", nil
	case 1:
		if st, err := os.Stat(args[0]); err != nil || !st.IsDir() {
			return "", fmt.Errorf("%s is not a directory", args[0])
		}
		return args[0], nil
	}
	return "", fmt.Errorf("give one directory, not %d: %s", len(args), strings.Join(args, " "))
}

// loadConfig reads the config file. It stops the program on an error.
func loadConfig() config.Config {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "shoulder:", err)
		os.Exit(2)
	}
	return cfg
}

// flagsSet returns the flags that the command line sets.
func flagsSet(fs *flag.FlagSet) map[string]bool {
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	return set
}

// defaults applies the config values of the flags that the command line
// does not set.
func defaults(set map[string]bool, cfg config.Config, since *time.Duration, all *bool) {
	if cfg.Since != nil && !set["since"] {
		*since = *cfg.Since
	}
	if cfg.All != nil && !set["all"] {
		*all = *cfg.All
	}
}

// editor returns the editor command: the config value, then VISUAL, then
// EDITOR, then vi.
func editor(cfg config.Config) string {
	for _, e := range []string{cfg.Editor, os.Getenv("VISUAL"), os.Getenv("EDITOR")} {
		if e == "" {
			continue
		}
		// omarchy-launch-editor puts "--" before its arguments, so the
		// editor reads "+LINE" as a file name. Run the editor that Omarchy
		// selects instead.
		if f := config.SplitCommand(e); len(f) > 0 && filepath.Base(f[0]) == "omarchy-launch-editor" {
			return omarchyEditor()
		}
		return e
	}
	return "vi"
}

// omarchyEditor returns the editor that Omarchy selects, the way
// omarchy-launch-editor finds it: the first line of its defaults file, or
// nvim when the file is missing or the editor is not installed.
func omarchyEditor() string {
	h, _ := os.UserHomeDir()
	ed := ""
	if b, err := os.ReadFile(filepath.Join(h, ".local", "state", "omarchy", "defaults", "editor")); err == nil {
		ed, _, _ = strings.Cut(string(b), "\n")
		ed = strings.TrimSpace(ed)
	}
	if ed == "sublime_text" {
		ed = "subl"
	}
	if _, err := exec.LookPath(ed); ed == "" || err != nil {
		ed = "nvim"
	}
	return ed
}
