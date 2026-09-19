---
name: screenshot
description: Take a real PNG screenshot of TunnelBoy's terminal dashboard (or any other tunnelboy subcommand's TUI). Use when asked for a screenshot/visual evidence of a dashboard change, a before/after comparison, or a picture to paste into a PR/Slack message. Can screenshot your real, current tunnel state, or an isolated fake state for safely demonstrating a specific scenario (e.g. a tunnel dying) without touching real tunnels or AWS.
---

# screenshot

TunnelBoy's `dash` is a Bubble Tea TUI — it takes over the terminal, so it
can't be screenshotted with a normal OS screen-capture tool from an agent
without either (a) needing Screen Recording permission and grabbing
whatever's actually on the user's screen, or (b) a fully self-contained
render. This skill does the latter: it drives the app inside a detached
`tmux` session, captures the pane's ANSI output, and renders that to a real
PNG with `ansi2png.py` — no display, no OS permissions, no risk of
capturing unrelated on-screen content.

All the mechanical work (build, tmux, ANSI parsing, PNG rendering) lives in
`capture.sh` and `ansi2png.py` in this directory — deterministic, no model
judgment needed at that layer. The judgment calls for whoever's driving this
skill are: which state to show (real vs. fake), what to click through to get
there, and what to name the output.

## Quick start — screenshot the real, current dashboard

```bash
.claude/skills/screenshot/capture.sh --title "tunnelboy dash" --out /path/to/out.png
```

This drives your **real** `~/.tunnelboy` state — whatever tunnels are
actually active or unmanaged right now is what shows up. Read-only by
default (just navigation), but if you pass `--keys`, remember it's driving
the real thing: don't send keys that confirm a destructive action (e.g.
`d,y` disconnects/kills whatever's under the cursor) unless that's actually
the intent.

## Screenshotting a specific scenario safely (`--fake`)

For "before/after" evidence of a code change — e.g. showing what a bug
fix's dashboard looks like — don't script destructive actions against the
real environment. Use `--fake`, which runs the app against a throwaway,
isolated `$HOME` (so its own `~/.tunnelboy` state is empty) instead. Nothing
under the real `~/.tunnelboy` is read or written.

Populate that fake state with `--seed FILE` — a shell script, sourced with
`$FAKE_HOME` set, that writes `.tunnelboy/tunnels/*.json` /
`.tunnelboy/logs/*.log` files before launch. `examples/seed-active-tunnel.sh`
in this directory is a ready-made one: it writes a fake "active" `rds-3307`
tunnel backed by a real (harmless) `sleep 600` process, so TunnelBoy's own
`IsAlive` check reports it as alive, and records that process's PID at
`$FAKE_HOME/fake.pid` (`capture.sh`'s cleanup always kills whatever PID is
there, so it can't leak past the run).

To demonstrate something changing mid-session — e.g. a tunnel dying — use
`--between CMD`, a shell command run after the app is up but before the
final capture. Pair it with `--between-wait-for TEXT`: the dashboard
refreshes at 1Hz, so a fixed sleep shorter than that is a race (this bit
us during development — a 1s sleep after killing a fake tunnel's PID
sometimes captured the frame *before* the dashboard's own next tick had
noticed). Poll for text that only appears once the change has landed
instead.

Worked example — the ACTIONS panel picking up a tunnel that died on its own
(this is exactly how the "started X but no active tunnels" bug's fix was
screenshotted):

```bash
.claude/skills/screenshot/capture.sh \
  --fake \
  --seed .claude/skills/screenshot/examples/seed-active-tunnel.sh \
  --between 'kill "$(cat "$FAKE_HOME/fake.pid")"' \
  --between-wait-for "vanished" \
  --title "tunnelboy dash — tunnel vanished" \
  --out /tmp/after.png
```

## Requirements

`capture.sh` checks for `tmux`, `go`, `python3`, and the Pillow python
package at the start of every run, and installs whatever's missing
(`brew install` for the CLI tools, `pip install --user` for Pillow) before
doing anything else — no separate setup step needed. Pass `--no-install` to
only check and fail loudly instead (e.g. in CI, or if you'd rather install
things yourself). Auto-install needs Homebrew; if it isn't present, the
script says so and exits rather than trying to bootstrap it.

## Reference

Run `capture.sh --help` for the full flag list. Key ones:

| Flag | Purpose |
|---|---|
| `--fake` | Isolated `$HOME`; real state untouched. Implied by `--seed`/`--between`. |
| `--seed FILE` | Sourced with `$FAKE_HOME` set, to write fake state before launch. |
| `--between CMD` | Runs after launch, before capture — e.g. to kill a fake PID. |
| `--between-wait-for TEXT` | Poll for this text after `--between` instead of a fixed sleep. |
| `--keys K1,K2,...` | tmux key names (`Down`, `Enter`, `y`, `Escape`, ...) sent in order before capture. |
| `--wait-for TEXT` | Text to poll for after launch before doing anything else (default `TUNNELBOY`). |
| `--cols` / `--rows` | tmux pane size (default `140x40`). |
| `--out PATH` | Output PNG path (default: a temp file; the path is always printed as the last line of stdout). |
| `--title TEXT` | Draws a fake macOS title bar with this text on the PNG. |
| `--no-install` | Only check requirements, don't install missing ones — fail instead. |
| `-- <subcommand> ...` | Run something other than `dash` (default). |

`ansi2png.py` can also be run standalone against any `tmux capture-pane -e -p`
dump: `ansi2png.py pane.ansi out.png [--title TEXT] [--font PATH]`. It
auto-detects a monospace TTF (macOS's SFNSMono, common Linux DejaVu/Liberation
paths); override with `--font` if none of those are found. It draws every
glyph in a fixed-width cell rather than measuring natural string width —
symbols like `●`/`•`/`↑↓` render wider than ASCII in most monospace fonts,
and drawing whole strings at their "expected" pixel width causes visible
drift/overlap on later columns. Don't revert to whole-string drawing without
re-checking a row that mixes a symbol with trailing plain text.
