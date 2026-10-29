# Changelog

## Unreleased

- Watch Auggie (Augment CLI) sessions in `~/.augment/sessions/`.

## v0.3.2 · 2026-10-31

This release fixes a crash, the Nix build, and agent states that did not change. It also makes the screen easier to read.

- The Nix build works again. `nix run github:bjarneo/shoulder` failed since v0.3.0, and CI now builds the flake.
- `d` no longer crashes shoulder after an agent makes the file of the free view shorter.
- An agent that stops during a command no longer shows `running` forever. A long call shows how long it runs, such as `47m · $ go test ./...`.
- A Claude Code turn that starts with a task notification shows the agent as working, not `done`.
- Codex: background commands, their output and repeated prompts show correctly. Resumed threads show again.
- Gemini CLI: a turn that ends with an error ends.
- A session with no activity in the `--since` period leaves the list.
- Each agent keeps its tag and its color. `1` … `9` pin by tag.
- A long path keeps its end, so the file name shows.
- The terminal pane shows the colors of the command output.
- Emoji and CJK text no longer break the layout.
- In a narrow terminal, the top bar shows a row of the agents.
- A key that does nothing shows a short note.
- The empty state counts the agents in other projects and shows the key that watches all projects.
- Syntax highlighting runs in the background, so a large file does not freeze the screen.
- A refresh of a repo with many untracked files takes 28 ms, down from 214 ms.
- Git status: copies, renames in the work tree, submodules and changes of line endings show correctly.
- The shell parser reads quotes, escapes, subshells and `cd -`.
- Flags after the directory work, such as `shoulder ~/proj --all`.
- The editor command can have quotes, such as a path with spaces.
- `shoulder status --json` escapes the Waybar text and tooltip.
- The config file can start with a byte order mark.
- `ctrl+c` quits while the help box is open.

## v0.3.1 · 2026-10-29

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
