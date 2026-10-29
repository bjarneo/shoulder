# shoulder

**Watch over your agents' shoulder.**

See your coding agents work, live. shoulder follows Claude Code, Codex, Gemini CLI, Auggie, opencode and omp through your project. It shows the file that each agent reads or edits, the diff against your last commit, and the commands that it runs.

[![Claude simplifies snake.js one line at a time while Codex adds tests. shoulder follows both.](site/poster.jpg)](https://bjarneo.github.io/shoulder/)

To see the 47-second demo, open the [website](https://bjarneo.github.io/shoulder/) or download [shoulder-demo.mp4](https://github.com/bjarneo/shoulder/releases/latest/download/shoulder-demo.mp4).

## Install

Install shoulder with Go 1.26 or later:

```sh
go install github.com/bjarneo/shoulder@latest
```

Or download a binary from the [latest release](https://github.com/bjarneo/shoulder/releases/latest). There are binaries for `linux-amd64`, `linux-arm64`, `darwin-amd64` and `darwin-arm64`:

```sh
curl -sL https://github.com/bjarneo/shoulder/releases/latest/download/shoulder-linux-amd64.tar.gz | tar xz
sudo mv shoulder-linux-amd64/shoulder /usr/local/bin/
```

To verify a download, compare it with `checksums.txt` in the release:

```sh
sha256sum -c checksums.txt --ignore-missing
```

With Nix, run shoulder from the flake, or enter a shell with Go and its tools for development:

```sh
nix run github:bjarneo/shoulder
nix develop
```

## Run

Run shoulder in the project where your agents work:

```sh
cd ~/Code/myproject
shoulder
```

Then start your agents in the same project. Each agent shows up within a few seconds. To watch a different project, give its path:

```sh
shoulder ~/Code/myproject
```

shoulder watches the git top level of the directory. It shows the sessions that started there or in a subdirectory.

| Flag | Default | Description |
|---|---|---|
| `--since` | `1h` | Show sessions with activity in this period, for example `30m` or `6h` |
| `--splits` | off | Start with one split per agent |
| `--signs` | off | Show changes as signs in the gutter, without the deleted lines |
| `--all` | off | Watch the agents of all projects, not only of this project |
| `--hide-done` | off | Hide the agents that are done |
| `--version` | | Print the version |

### Set the defaults

To change the defaults of the flags, set them at the top of `~/.config/shoulder/config.toml`. A flag on the command line has priority over the file.

```toml
since = "6h"
all = true
hide_done = true
signs = false
splits = false
editor = "nvim"
```

`editor` is the command that `e` runs. Without it, shoulder uses `VISUAL`, then `EDITOR`, then `vi`. The same file holds the [keys](#change-the-keys).

## Supported agents

shoulder reads the session logs that the agents already write. You do not configure the agents.

| Agent | Session data | To use another location, set |
|---|---|---|
| Claude Code, with subagents | `~/.claude/projects/` | `CLAUDE_CONFIG_DIR` |
| Codex | `~/.codex/sessions/` | `CODEX_HOME` |
| Gemini CLI | `~/.gemini/tmp/` | `GEMINI_CLI_HOME` |
| Auggie (Augment CLI) | `~/.augment/sessions/` | — |
| opencode 2.x | `~/.local/share/opencode/opencode.db` | `XDG_DATA_HOME` |
| omp | `~/.omp/agent/sessions/` | `PI_CODING_AGENT_DIR` |

shoulder only reads. It does not send input to an agent, it does not change files, and it does not connect to the network. It opens the opencode database read-only.

## Screen

- The left panel lists the agents and the changed files. A dot next to a file shows which agent is in that file now.
- Each agent has a tag, such as `A1`, and a color. The agent keeps its tag and its color while it is in the list.
- When the terminal is too narrow for the left panel, the top bar shows a row of the agents.
- The center pane shows the file of the focused agent. A cursor and a label, such as `● A1 editing`, show where the agent works.
- In the inline diff, deleted lines show in red above the new lines. Added and changed lines have a green background. Press `d` to show the changes as signs in the gutter.
- The terminal pane under the file shows the commands of the focused agent and their output, with the colors of the output. A command with more lines, such as a script in a heredoc, shows its first 5 lines.
- The right panel shows the activity log of the focused agent: reads, searches, edits, commands, replies and prompts.
- Press `s` for the split layout. Each agent gets a pane with its file and its activity log.

The bar under each agent shows how full its context window is.

To hide the agents that are done, press `x` or start with `--hide-done`. An agent stays for 1 minute after it finishes, and the focused agent always stays. The `✓` count in the sidebar includes the hidden agents.

## Open the file in your editor

To open the file at the cursor in your editor, press `e`. shoulder gives the terminal to the editor and comes back when the editor closes. Most terminal editors get `+LINE FILE`. `zed`, `hx` and `subl` get `FILE:LINE`. VS Code and Cursor get `-g FILE:LINE`.

When `EDITOR` is `omarchy-launch-editor`, shoulder runs the editor that Omarchy selects. The wrapper puts `--` before its arguments, so the editor would read `+LINE` as a file name.

To copy `path:line` of the cursor, press `y`. The copy uses OSC 52. In tmux, add `set -g set-clipboard on` to your tmux configuration.

## History

To step back through the activity log of the focused agent, press `,`. To step forward, press `.`. You can also click an entry in the activity log.

- The center pane shows the file at the read or the edit of the step.
- The terminal pane shows the commands up to the step.
- The status line shows `HISTORY`.

A step past the newest entry, or `esc`, goes back to follow. The center pane shows each file as it is now, not as it was at the step.

### While you were away

When you come back to the terminal after 30 seconds or more, the message line lists what each agent did. An example is `while you were away · A1 claude edited 3 files · A2 codex asks you`. A line in the activity log marks the start of the time away.

This needs a terminal that reports focus changes. In tmux, add `set -g focus-events on` to your tmux configuration.

## Auto focus

Auto focus is on when shoulder starts. The view jumps to the work of the agents:

- When an agent edits a file in the project, the focus goes to that agent and its file.
- When an agent edits a file through the shell, such as with `sed -i`, a script or `gofmt -w`, the focus goes to that agent and the changed lines. See [Commands](#commands).
- When an agent asks you a question or waits for your approval, the focus goes to that agent.
- When the focused agent finishes, the focus goes to an agent that still works in the project.

shoulder waits at least 1.5 seconds between two jumps, so agents that work at the same time do not make the view flicker. In free scroll, the view does not jump.

To keep the focus on one agent, select it with `1` … `9`, `tab` or a click. The key `3` selects `A3`. This pins the agent and turns auto focus off. To turn auto focus on again, press `a`. The status line shows `auto` or `pinned`.

## Commands

shoulder follows the commands that agents run in the shell:

- `cat`, `head`, `sed -n` and `nl | sed -n` count as reads of a file. The view goes to the lines that the agent reads.
- `rg`, `grep` and `git grep` count as searches.
- A command with more parts, such as `cat a.go && sed -n 1,80p b.go`, gives a read for each file. A `cd DIR` part changes the directory of the paths after it.
- When a file in `git status` changes on disk and no edit tool call explains the change, the edit goes to the agent that ran a command at that time.

## All projects

To watch the agents of all projects, press `A` or start shoulder with `--all`. To go back to the agents of this project, press `A` again.

- The row of an agent from another project starts with the name of that project, such as `preamp · Add a preset`.
- The top line shows `all projects`.
- The terminal pane shows the project of the agent in the prompt.
- A file in another git repository shows the diff against the `HEAD` of that repository.

Auto focus works the same way. It jumps to new edits in all projects.

## Status bar

`shoulder status` prints one line with the number of agents that work, that wait for you and that are done. With no agents, the line is empty.

```sh
$ shoulder status
●2 ○1 ✓3
```

It takes the flags `--since`, `--all` and `--json`, and a directory. It reads only the end of each session log, so it is fast enough for a status bar.

To show the agents of all projects in Waybar, add a custom module:

```jsonc
"custom/shoulder": {
  "exec": "shoulder status --all --json",
  "return-type": "json",
  "interval": 5
}
```

The JSON has the class `waiting`, `active`, `done` or `none`. The tooltip lists each agent. To color the module when an agent waits for you, add this to the Waybar style:

```css
#custom-shoulder.waiting { color: #e0af68; }
```

To show the agents of the current project in tmux, add this to your tmux configuration:

```sh
set -g status-right '#(shoulder status #{q:pane_current_path})'
set -g status-interval 5
```

## Agent states

- `●` The agent runs a tool or writes a reply.
- `○` The agent waits for you. It asked a question, or a tool call needs your approval.
- `✓` The turn ended. The state is `idle` when the agent had no activity for 10 minutes.

A command, a task or a web call that runs for 1 minute or more shows how long it runs, such as `47m · $ go test ./...`. A wait for you shows how long it waits. When a call has no result and the session log gets no write for 30 minutes, the agent stopped. Its state is then `idle`, with `no result`.

A session with no activity in the `--since` period leaves the list. It comes back when the agent works again.

Gemini CLI logs when a tool call waits for approval. For the other agents, shoulder assumes approval when a file tool has no result after 20 seconds.

## Keys

| Key | Action |
|---|---|
| `1` … `9` | Pin the agent with that tag, such as `A3` for `3` |
| `tab`, `shift+tab` | Pin the next or the previous agent |
| `a` | Turn auto focus on or off |
| `A` | Switch between the agents of this project and of all projects |
| `x` | Hide or show the agents that are done |
| `f` | Toggle follow and free scroll |
| `esc` | Go back to follow |
| `j`, `k` | Move the cursor |
| `ctrl+d`, `ctrl+u` | Move half a page down or up |
| `gg`, `G` | Go to the top or the bottom of the file |
| `]c`, `[c` | Go to the next or the previous change |
| `H`, `L`, `gT`, `gt` | Open the previous or the next file tab |
| `,`, `.` | Step to the older or the newer entry of the activity log |
| `e` | Open the file at the cursor in your editor |
| `y` | Copy `path:line` of the cursor |
| `s` | Toggle the follow layout and the split layout |
| `d` | Toggle the inline diff and the signs |
| `R` | Read all sessions again |
| `?` | Show all keys |
| `q` | Quit |

You can also click an agent, a file, a tab or a split pane.

### Change the keys

To change a key, add a `[keys]` table to `~/.config/shoulder/config.toml`. To use a different file, set `XDG_CONFIG_HOME` or `SHOULDER_CONFIG`. Without the file, shoulder uses the default keys.

```toml
[keys]
hunk_next = "n"
hunk_prev = "p"
top = ["gg", "home"]
reload = []
```

- Give 1 key or an array of keys. An empty array removes all keys of the action.
- Write a sequence of one-character keys without spaces, such as `gg` or `]c`. Put spaces between the keys of a longer sequence, such as `"ctrl+w j"` or `"g g g"`.
- Write a named key the way Bubble Tea names it, such as `ctrl+n`, `alt+x`, `shift+tab`, `enter`, `space`, `pgdown` or `f5`.
- When the config binds an action, the action loses its default keys. When another action has the same key by default, that action loses the key. A default sequence that starts with your key also goes away. For example, `top = "g"` removes `gt` and `gT`.
- The keys `1` … `9` and `ctrl+c` do not change.

shoulder stops with an error for an unknown action, an unknown key, or 2 actions on the same key. The help screen and the sidebar show the keys from your config.

| Action | Default keys |
|---|---|
| `agent_next`, `agent_prev` | `tab`, `shift+tab` |
| `auto` | `a` |
| `all_projects` | `A` |
| `hide_done` | `x` |
| `follow` | `f` |
| `back` | `esc` |
| `down`, `up` | `j` `down`, `k` `up` |
| `page_down`, `page_up` | `ctrl+d` `pgdown`, `ctrl+u` `pgup` |
| `top`, `bottom` | `gg` `home`, `G` `end` |
| `hunk_next`, `hunk_prev` | `]c`, `[c` |
| `buffer_next`, `buffer_prev` | `L` `gt`, `H` `gT` |
| `history_prev`, `history_next` | `,`, `.` |
| `open_editor` | `e` |
| `copy_path` | `y` |
| `splits` | `s` |
| `inline_diff` | `d` |
| `reload` | `R` |
| `help` | `?` |
| `quit` | `q` |

## Colors

shoulder uses the colors of your terminal theme. Text uses the default colors and the 16 ANSI colors, so it changes when you change the theme.

The panel shades and the line highlights are mixed from the colors that the terminal reports. shoulder asks the terminal again every 3 seconds, so a theme change shows while shoulder runs. If your terminal does not report its colors, the panels use the terminal background and the change signs show the diff.

In tmux, shades show correctly only when tmux supports true color. To turn it on, add `set -as terminal-features ",*:RGB"` to your tmux configuration.

## Diff baseline

In a git repository, shoulder compares each file with `HEAD`. Outside git, it compares each file with the first content that it read.

## Limits

- shoulder reads opencode 2.x. It does not read the older `part` tables of opencode 1.x.
- For Claude Code, Codex, Auggie, opencode and omp, the approval state is an estimate. See [Agent states](#agent-states).
- When omp moves a command to the background, shoulder shows it as running until omp writes the output of the finished job.
- Auggie saves its session after each model response, so shoulder sees a tool call when the response ends, and its result when the next response ends.
- shoulder reads Auggie sessions only in `~/.augment/`. It does not find the sessions of an auggie that runs with `--augment-cache-dir`.
- shoulder finds edits through the shell only for files in the `git status` of this project. When you edit a file in your editor while an agent runs a command, the edit also goes to that agent.
- In all projects, `FILES` shows the git changes of this project only. For the other projects, it shows the files that agents edited.
- The binaries are for Linux and macOS. shoulder is not tested on Windows.

## Develop

```sh
go test ./...
go build -o shoulder .
```

The code has three packages:

- `internal/watch` reads the session logs of each agent and turns them into one event model.
- `internal/repo` reads the project files and compares them with the last commit.
- `internal/ui` draws the TUI with Bubble Tea.

To record the demo video again, run `demo/record.py`. The script header lists what it needs.

## License

MIT. See [LICENSE](LICENSE).
