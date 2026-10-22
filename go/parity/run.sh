#!/bin/sh
# Builds orch and runs the parity harness against the JS runtime. The default reference is the
# commit in parity/js-ref, the last main commit before S12 (the JS the Go port was checked
# against); its orchestrator/ is extracted from git into a temp dir for the run.
#   parity/run.sh               every group, plus TestJSReference
#   parity/run.sh --only json   one group (TestParity/<group>); --only json,usage for several
#   parity/run.sh --only jsref  TestJSReference alone; --only json,jsref for both
#   ORCH_JS=/path/to/orchestrator parity/run.sh   compare against a JS tree (wins over ORCH_JS_REF)
#   ORCH_JS_REF=v0.5.0 parity/run.sh              compare against orchestrator/ at another commit-ish
set -eu

module_dir=$(cd "$(dirname "$0")/.." && pwd)
repo_dir=$(dirname "$module_dir")

group=""
only_given=false
while [ $# -gt 0 ]; do
  case "$1" in
    --only)
      [ $# -ge 2 ] || { echo "usage: parity/run.sh [--only GROUP]" >&2; exit 2; }
      group=$2
      only_given=true
      shift 2
      ;;
    *)
      echo "usage: parity/run.sh [--only GROUP]" >&2
      exit 2
      ;;
  esac
done

parity_groups=""
want_jsref=false
for name in $(echo "$group" | tr , ' '); do
  if [ "$name" = jsref ]; then
    want_jsref=true
  else
    parity_groups="${parity_groups:+$parity_groups|}$name"
  fi
done
if [ "$only_given" = true ] && [ -z "$parity_groups" ] && [ "$want_jsref" = false ]; then
  echo "usage: parity/run.sh [--only GROUP]" >&2
  exit 2
fi

if [ -z "${ORCH_JS:-}" ]; then
  if [ -n "${ORCH_JS_REF:-}" ]; then
    ref=$ORCH_JS_REF
  else
    ref=$(cat "$module_dir/parity/js-ref" 2>/dev/null) || ref=
    # Only a full SHA is immutable; a branch or tag here would move the oracle silently.
    case $ref in
      *[!0-9a-f]*) ref_ok=false ;;
      *) [ ${#ref} -eq 40 ] && ref_ok=true || ref_ok=false ;;
    esac
    if [ "$ref_ok" != true ]; then
      echo "go/parity/js-ref must hold one full commit SHA" >&2
      exit 2
    fi
  fi
  if ! git -C "$repo_dir" rev-parse --verify --quiet "$ref^{commit}" >/dev/null; then
    echo "parity reference $ref not found locally; run: git fetch --no-tags origin $ref (for a tag: git fetch origin tag $ref), or set ORCH_JS to a JS tree" >&2
    exit 2
  fi
  js_tree=$(mktemp -d)
  trap 'rm -rf "$js_tree"' EXIT
  trap 'exit 130' INT
  trap 'exit 143' TERM
  git -C "$repo_dir" archive "$ref" orchestrator | tar -x -C "$js_tree"
  ORCH_JS="$js_tree/orchestrator"
  [ -d "$ORCH_JS" ] || { echo "parity reference $ref has no orchestrator/ tree" >&2; exit 2; }
fi

cd "$module_dir"
CGO_ENABLED=0 go build -trimpath -o bin/orch ./cmd/orch

ORCH_BIN="$module_dir/bin/orch"
export ORCH_BIN ORCH_JS

# Not exec: the EXIT trap must still remove the extracted tree. set -e is suspended inside the
# if, so the first failing test status is kept and returned.
status=0
run_tests() {
  if go test -tags parity -count=1 -v ./parity -run "$1"; then
    return 0
  else
    code=$?
  fi
  [ "$status" -ne 0 ] || status=$code
}

if [ "$only_given" = false ]; then
  run_tests '^(TestParity|TestJSReference)$'
else
  # One -run pattern cannot do both: its subtest element would filter TestJSReference's subtests.
  if [ -n "$parity_groups" ]; then
    run_tests "^TestParity\$/^($parity_groups)\$"
  fi
  if [ "$want_jsref" = true ]; then
    run_tests '^TestJSReference$'
  fi
fi
exit "$status"
