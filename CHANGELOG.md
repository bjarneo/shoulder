# Changelog

## Unreleased

- Change the keys in `~/.config/shoulder/config.toml`. The help screen and the sidebar show your keys. See [Change the keys](README.md#change-the-keys). (#4)
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
