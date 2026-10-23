#!/bin/sh
# Stand-in for `orch <harness>-exec` in the worker's unit tests: the tests run the worker with
# this script as its own executable, so $1 is the bridge subcommand. It acts on FAKE_*:
#   FAKE_LOG       directory receiving argv (one argument per line), stdin and signals
#   FAKE_NO_STDIN  set: never read stdin (the worker's prompt meets a closed pipe)
#   FAKE_WAIT      set: run a foreground child until a signal reaches the process group; the
#                  child and this script each log the signal they got to $FAKE_LOG/signals,
#                  the child touches $FAKE_LOG/ready once its traps are set, and this script
#                  then waits for $FAKE_LOG/release before it goes on (each wait gives up
#                  after about 10 s, so a failed test leaves no process behind)
#   FAKE_HOLD      set: wait about 10 s with no trap set, so a SIGINT or SIGTERM that reaches
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
  trap 'echo "bridge TERM" >> "$log/signals"' TERM
  trap 'echo "bridge INT" >> "$log/signals"' INT
  sh -c '
    trap "echo \"child TERM\" >> \"$1/signals\"; exit 0" TERM
    trap "echo \"child INT\" >> \"$1/signals\"; exit 0" INT
    : > "$1/ready"
    i=0; while [ $i -lt 200 ]; do sleep 0.05; i=$((i + 1)); done' child "$log"
  i=0; while [ ! -f "$log/release" ] && [ $i -lt 200 ]; do sleep 0.05; i=$((i + 1)); done
fi
if [ -n "$FAKE_HOLD" ]; then
  i=0; while [ $i -lt 200 ]; do sleep 0.05; i=$((i + 1)); done
fi
printf '%s' "$FAKE_STDERR" >&2
printf '%s' "$FAKE_STDOUT"
exit "${FAKE_EXIT:-0}"
