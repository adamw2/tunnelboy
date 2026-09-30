#!/usr/bin/env bash
# Example --seed script for capture.sh --fake: writes one fake "active" rds
# tunnel, backed by a real (harmless) `sleep` process so IsAlive reports it
# as alive. Sourced with $FAKE_HOME already created; records the sleep's
# PID at $FAKE_HOME/fake.pid, which capture.sh's cleanup always kills, and
# which a --between command can also kill early to simulate the tunnel's
# process dying unexpectedly (see README.md's vanish-detection example).
set -euo pipefail

sleep 600 &
FAKE_PID=$!
echo "$FAKE_PID" > "$FAKE_HOME/fake.pid"

cat > "$FAKE_HOME/.tunnelboy/tunnels/rds-3307.json" <<JSON
{
  "id": "rds-3307",
  "pid": $FAKE_PID,
  "type": "rds",
  "target": "prod-pes-db",
  "local_port": 3307,
  "profile": "prodread",
  "detached": true,
  "status": "active",
  "started_at": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "log_file": "$FAKE_HOME/.tunnelboy/logs/rds-3307.log"
}
JSON

cat > "$FAKE_HOME/.tunnelboy/logs/rds-3307.log" <<LOG
Starting session with SessionId: fake-session-id
Port 3307 opened. Waiting for connections...
LOG
