#!/usr/bin/env bash
# Screenshots the TunnelBoy TUI to a PNG. See SKILL.md for usage from an
# agent's perspective; this is the deterministic part (build, tmux, render).
set -euo pipefail

SKILL_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(git -C "$SKILL_DIR" rev-parse --show-toplevel)"
SESSION="tunnelboy-screenshot-$$"

NO_INSTALL=0
FAKE=0
SEED=""
BETWEEN=""
BETWEEN_WAIT_FOR=""
KEYS=""
WAIT_FOR="TUNNELBOY"
SETTLE=1
COLS=140
ROWS=40
OUT=""
TITLE=""
SUBCMD=(dash)

usage() {
  cat <<'EOF' >&2
Usage:
  capture.sh [options] [-- <tunnelboy subcommand and args, default: dash>]

Options:
  --fake              Run against a throwaway, isolated $HOME instead of
                      the real ~/.tunnelboy — nothing real is read or
                      touched. Required if --seed or --between is used.
  --seed FILE         Shell snippet, sourced with $FAKE_HOME set, to
                      populate fake state before launch (e.g. write a
                      .tunnelboy/tunnels/*.json). Implies --fake.
  --between CMD       Shell command run after the app is ready but before
                      capture (e.g. `kill $(cat "$FAKE_HOME/pid")` to
                      simulate a tunnel dying mid-session). Implies --fake.
  --between-wait-for TEXT
                      Text to poll for (like --wait-for) after --between,
                      instead of a fixed sleep — use this whenever --between
                      is meant to change what's on screen (e.g. wait for
                      "vanished" after killing a fake tunnel's PID), since
                      the dashboard's own 1Hz refresh means a fixed sleep
                      shorter than that is a race.
  --keys K1,K2,...    tmux send-keys, applied in order (tmux key names:
                      Down, Enter, y, Escape, ...), each followed by a
                      --settle pause, before the final capture.
  --wait-for TEXT     Text to poll for before capturing (default: TUNNELBOY).
  --settle SECONDS    Pause after launch/each key/--between (default: 1).
  --cols N / --rows N tmux pane size (default: 140x40).
  --out PATH          Output PNG path (default: a temp file; path is
                      printed on the last stdout line either way).
  --title TEXT        Fake title-bar text drawn on the PNG.
  --no-install        Only check requirements (tmux, go, python3, Pillow),
                      don't try to install anything missing — fail instead.

Requirements: tmux, go, python3, and the Pillow python package. Missing
ones are installed automatically via Homebrew/pip unless --no-install is
given (see the preflight() function below for exactly what runs).

Safety: without --fake, this drives your REAL tunnelboy state — it will
show your actual active/unmanaged tunnels. Never pass --keys that
confirms a destructive action (e.g. "d,y") against real state unless you
mean to disconnect/kill something for real.
EOF
  exit "${1:-0}"
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --fake) FAKE=1; shift ;;
    --seed) SEED="$2"; FAKE=1; shift 2 ;;
    --between) BETWEEN="$2"; FAKE=1; shift 2 ;;
    --between-wait-for) BETWEEN_WAIT_FOR="$2"; shift 2 ;;
    --keys) KEYS="$2"; shift 2 ;;
    --wait-for) WAIT_FOR="$2"; shift 2 ;;
    --settle) SETTLE="$2"; shift 2 ;;
    --cols) COLS="$2"; shift 2 ;;
    --rows) ROWS="$2"; shift 2 ;;
    --out) OUT="$2"; shift 2 ;;
    --title) TITLE="$2"; shift 2 ;;
    --no-install) NO_INSTALL=1; shift ;;
    -h|--help) usage 0 ;;
    --) shift; SUBCMD=("$@"); break ;;
    *) echo "unknown arg: $1" >&2; usage 1 ;;
  esac
done

# require_cmd checks $1 is on PATH; if not, installs Homebrew formula $2
# (default: $1) unless --no-install was passed, in which case it fails.
require_cmd() {
  local cmd="$1" formula="${2:-$1}"
  command -v "$cmd" >/dev/null 2>&1 && return 0

  if [[ "$NO_INSTALL" == 1 ]]; then
    echo "missing required command '$cmd' (brew formula '$formula') and --no-install was given" >&2
    exit 1
  fi
  if ! command -v brew >/dev/null 2>&1; then
    echo "missing required command '$cmd', and Homebrew isn't installed to fetch it automatically." >&2
    echo "install Homebrew (https://brew.sh) or '$formula' yourself, then retry." >&2
    exit 1
  fi
  echo "installing missing dependency '$formula' via Homebrew..." >&2
  brew install "$formula" >&2
  command -v "$cmd" >/dev/null 2>&1 || { echo "installed '$formula' but '$cmd' still isn't on PATH" >&2; exit 1; }
}

# require_pillow checks the Pillow python module is importable; if not,
# installs it via pip unless --no-install was passed.
require_pillow() {
  python3 -c 'import PIL' >/dev/null 2>&1 && return 0

  if [[ "$NO_INSTALL" == 1 ]]; then
    echo "missing required python package 'Pillow' and --no-install was given" >&2
    exit 1
  fi
  echo "installing missing dependency 'Pillow' via pip..." >&2
  # Homebrew's python3 treats the interpreter as externally-managed (PEP
  # 668) and refuses a plain --user install; --break-system-packages is
  # safe here since Pillow is only ever imported by this skill's own
  # ansi2png.py, never by anything else on the system.
  python3 -m pip install --user --quiet Pillow >&2 \
    || python3 -m pip install --user --quiet --break-system-packages Pillow >&2
  python3 -c 'import PIL' >/dev/null 2>&1 \
    || { echo "installed Pillow but it still isn't importable — install it yourself: python3 -m pip install --user Pillow" >&2; exit 1; }
}

preflight() {
  require_cmd tmux
  require_cmd go
  require_cmd python3 python3
  require_pillow
}

preflight

WORK="$(mktemp -d)"
BIN="$WORK/tunnelboy"
ANSI="$WORK/pane.ansi"
cleanup() {
  tmux kill-session -t "$SESSION" >/dev/null 2>&1 || true
  # A --seed script may have started a real background process (e.g. a
  # `sleep` standing in for a tunnel's PID) and recorded it here — make sure
  # it doesn't outlive this run.
  if [[ -n "${FAKE_HOME:-}" && -f "$FAKE_HOME/fake.pid" ]]; then
    kill "$(cat "$FAKE_HOME/fake.pid")" 2>/dev/null || true
  fi
  rm -rf "$WORK"
}
trap cleanup EXIT

echo "building..." >&2
go build -o "$BIN" "$REPO_ROOT/cmd/tunnelboy"

RUN_ENV=()
if [[ "$FAKE" == 1 ]]; then
  FAKE_HOME="$WORK/home"
  mkdir -p "$FAKE_HOME/.tunnelboy/tunnels" "$FAKE_HOME/.tunnelboy/logs"
  export FAKE_HOME
  if [[ -n "$SEED" ]]; then
    # shellcheck disable=SC1090
    source "$SEED"
  fi
  RUN_ENV=(env "HOME=$FAKE_HOME")
  echo "using isolated fake HOME: $FAKE_HOME (real ~/.tunnelboy untouched)" >&2
fi

tmux kill-session -t "$SESSION" >/dev/null 2>&1 || true
tmux new-session -d -s "$SESSION" -x "$COLS" -y "$ROWS" "${RUN_ENV[@]}" "$BIN" "${SUBCMD[@]}"

wait_for() {
  local needle="$1" deadline=$((SECONDS + 10))
  while (( SECONDS < deadline )); do
    if tmux capture-pane -t "$SESSION" -p 2>/dev/null | grep -qF "$needle"; then
      return 0
    fi
    sleep 0.2
  done
  echo "timed out waiting for '$needle' — pane so far:" >&2
  tmux capture-pane -t "$SESSION" -p >&2 || true
  exit 1
}

wait_for "$WAIT_FOR"
sleep "$SETTLE"

if [[ -n "$BETWEEN" ]]; then
  eval "$BETWEEN"
  if [[ -n "$BETWEEN_WAIT_FOR" ]]; then
    wait_for "$BETWEEN_WAIT_FOR"
  else
    # No explicit marker to poll for: the dashboard's own refresh is 1Hz, so
    # sleeping less than ~2s risks capturing before it notices the change.
    sleep "$((SETTLE + 2))"
  fi
fi

if [[ -n "$KEYS" ]]; then
  IFS=',' read -ra KEY_ARR <<< "$KEYS"
  for k in "${KEY_ARR[@]}"; do
    tmux send-keys -t "$SESSION" "$k"
    sleep "$SETTLE"
  done
fi

tmux capture-pane -t "$SESSION" -e -p > "$ANSI"

tmux send-keys -t "$SESSION" q >/dev/null 2>&1 || true
tmux kill-session -t "$SESSION" >/dev/null 2>&1 || true

if [[ -z "$OUT" ]]; then
  OUT="$(mktemp -t tunnelboy-screenshot).png"
fi

TITLE_ARGS=()
[[ -n "$TITLE" ]] && TITLE_ARGS=(--title "$TITLE")
python3 "$SKILL_DIR/ansi2png.py" "$ANSI" "$OUT" "${TITLE_ARGS[@]}"
