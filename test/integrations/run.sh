#!/usr/bin/env bash
# Runs every integration guide in docs/integrations against a real clrd.
#
#   CLRD_BIN=/path/to/clrd test/integrations/run.sh          # every case
#   CLRD_BIN=/path/to/clrd test/integrations/run.sh gatus    # one case
#
# Each case in cases/ gets a fresh server and database, so cases never see
# each other's notifications. Adding a guide = adding cases/<name>.sh; adding
# a ?template= = adding a directory under templates/ (see README.md).
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$HERE/../.." && pwd)
BIN=$(realpath "${CLRD_BIN:?set CLRD_BIN to a built clrd binary}")
RUN_ID=clrd-it-$$
export HERE ROOT RUN_ID
# shellcheck source=lib.sh
. "$HERE/lib.sh"
set +e

if [ $# -gt 0 ]; then names=("$@"); else names=(); for f in "$HERE"/cases/*.sh; do names+=("$(basename "$f" .sh)"); done; fi

stop() {
  [ -f "$WORK/pids" ] && xargs -r kill <"$WORK/pids" 2>/dev/null
  docker ps -aq --filter "label=$RUN_ID" | xargs -r docker rm -f >/dev/null
  kill "$server" 2>/dev/null
  wait "$server" 2>/dev/null
}
trap 'stop; exit 1' INT TERM

failed=()
for name in "${names[@]}"; do
  WORK=$(mktemp -d)
  CLRD_PORT=$(free_port)
  CLRD=http://127.0.0.1:$CLRD_PORT
  export WORK CLRD CLRD_PORT
  export CLRD_DB=$WORK/clrd.db CLRD_LISTEN=127.0.0.1:$CLRD_PORT CLRD_BASE_URL=$CLRD
  CLRD_TOKEN=$("$BIN" token create --name "$name" --scope admin 2>/dev/null)
  export CLRD_TOKEN
  "$BIN" >"$WORK/server.log" 2>&1 &
  server=$!
  echo "== $name"
  # A hung tool must not hold the shared runner: 5 minutes per case.
  timeout 300 bash -c '. "$HERE/lib.sh"; wait_http "$CLRD/healthz"; . "$1"' _ "$HERE/cases/$name.sh"
  rc=$?
  stop
  if [ $rc = 0 ]; then
    echo "PASS $name"
  else
    echo "FAIL $name (exit $rc); server log:"
    tail -20 "$WORK/server.log"
    failed+=("$name")
  fi
  rm -rf "$WORK"
done

[ ${#failed[@]} = 0 ] || { echo "failed: ${failed[*]}"; exit 1; }
echo "all ${#names[@]} cases passed"
