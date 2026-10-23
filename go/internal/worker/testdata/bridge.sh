#!/bin/sh
# Stand-in for `orch <harness>-exec` in the worker's unit tests: the tests run the worker with
# this script as its own executable, so $1 is the bridge subcommand. It acts on FAKE_*:
#   FAKE_LOG       directory receiving argv (one argument per line), stdin and diagnostics
#   FAKE_NO_STDIN  set: never read stdin (the worker's prompt meets a closed pipe)
#   FAKE_WAIT      set: run a foreground child until a signal reaches the process group; the
#                  child and bridge ACK on events; ready follows both installed handlers.
#                  ready-go, child-go and bridge-go are blocking FIFO gates owned by the
#                  test, whose cleanup releases all gates and waits for the foreground tree.
#                  Value child-exit makes the child exit before ready for cleanup coverage.
#                  At most five events for one signal; keep signalFixtureMaxEvents in sync.
#   FAKE_HOLD      set: block on bridge-go with no trap, so a SIGINT or SIGTERM that reaches
#                  it at any point from its exec on kills it
#   FAKE_GATE      set: print "gate" on stderr, hold the FIFO $FAKE_LOG/go open, write
#                  "started" to the FIFO $FAKE_LOG/started, then wait for a line on go (the
#                  tests' pipe handshake); no trap, so the SIGTERM its runner forwards ends it
#                  wherever it blocks
#   FAKE_STDERR    printed on stderr, then FAKE_STDOUT on stdout
#   FAKE_EXIT      exit code (default 0)
log=$FAKE_LOG
for arg in "$@"; do printf '%s\n' "$arg" >> "$log/argv"; done
if [ -z "$FAKE_NO_STDIN" ]; then cat > "$log/stdin"; fi
if [ -n "$FAKE_GATE" ]; then
  echo gate >&2
  exec 3<> "$log/go"
  echo started > "$log/started"
  read -r _ <&3
  exec 3<&-
fi
if [ -n "$FAKE_WAIT" ]; then
  echo "$$" > "$log/bridge-pid"
  exec 4> "$log/events"
  trap 'echo "bridge TERM" >&4' TERM
  trap 'echo "bridge INT" >&4' INT
  sh -c '
    echo "$$" > "$1/child-pid"
    trap "echo \"child TERM\" >&4; exit 0" TERM
    trap "echo \"child INT\" >&4; exit 0" INT
    echo child-started >&4
    read -r _ < "$1/ready-go"
    if [ "$FAKE_WAIT" = child-exit ]; then
      echo child-exit >&4
      exit 0
    fi
    echo ready >&4
    read -r _ < "$1/child-go"' child "$log"
  echo child-done >&4
  read -r _ < "$log/bridge-go"
  exec 4>&-
fi
if [ -n "$FAKE_HOLD" ]; then
  read -r _ < "$log/bridge-go"
fi
printf '%s' "$FAKE_STDERR" >&2
printf '%s' "$FAKE_STDOUT"
exit "${FAKE_EXIT:-0}"
