# Changelog

## Unreleased

- `e` opens the file in one buffer at the cursor line when `EDITOR` is `omarchy-launch-editor`. Before, Neovim opened an empty buffer named `+LINE` and the file in a second buffer.

## v0.3.0 · 2026-10-29

shoulder now lets you hide finished agents, step through the history of an agent, open files in your editor, and show the agents in a status bar.

- Hide the agents that are done. Press `x`, or start with `--hide-done`.
- Open the file at the cursor in your editor with `e`, and copy `path:line` with `y`.
- Step through the activity log of an agent with `,` and `.`, or click an entry. The view shows the file and the terminal at that step.
- When you come back to the terminal, the message line lists what the agents did while you were away.
- Set the defaults of the flags and the editor in `~/.config/shoulder/config.toml`.
- `shoulder status` prints one line for Waybar or tmux. `--json` gives the format of a Waybar custom module.
- The terminal pane shows the first 5 lines of a command with more lines, such as a script in a heredoc.

## v0.2.0 · 2026-10-29

shoulder now follows agents through the shell, watches all projects, and lets you change the keys.

- Change the keys in `~/.config/shoulder/config.toml`. The help screen and the sidebar show your keys. See [Change the keys](https://github.com/bjarneo/shoulder#change-the-keys). (#4)
- Watch the agents of all projects. Press `A`, or start shoulder with `--all`. Each agent row shows the project of the agent.
- Follow edits through the shell. When a file changes on disk while an agent runs a command, such as `sed -i` or a script, the view goes to that agent and the changed lines.
- A command with more parts, such as `cat a.go && sed -n 1,80p b.go`, gives a read for each file.
- Auto focus goes to an agent that asks you a question or waits for your approval.
- Watch omp sessions. (#3)
- Run shoulder with Nix: `nix run github:bjarneo/shoulder`. (#2)
- `go install` builds print the module version for `shoulder --version`.

## v0.1.0 · 2026-10-27

The first release of shoulder. It follows your coding agents through your project, live, in your terminal.

- Watch Claude Code, Codex, Gemini CLI and opencode 2.x sessions in one project. Claude Code subagents show as their own agents.
- See the file that an agent reads or edits, with its cursor. Deleted lines show inline, and added and changed lines are highlighted against `HEAD`.
- See the commands of each agent and their output in the terminal pane, and each step in the activity log.
- Follow one agent, or press `s` for one split per agent.
- Auto focus jumps to new edits and away from finished agents. Press `1` … `9` to pin an agent and `a` to turn auto focus on again.
- Use the colors of your terminal theme. A theme change shows while shoulder runs.

The release has binaries for Linux and macOS on amd64 and arm64, a `checksums.txt` file, and the demo video.
