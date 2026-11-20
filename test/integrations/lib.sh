# Helpers for integration cases. run.sh sources this before each case, with
# CLRD (base URL), CLRD_PORT, CLRD_TOKEN (admin), WORK (scratch dir) and
# RUN_ID (container label) set. Needs bash, curl, jq and docker.
set -euo pipefail

fail() { echo "FAIL: $*" >&2; exit 1; }
ok() { echo "ok: $*"; }

# api PATH [curl args]: authenticated call, fails on any HTTP error.
api() { curl -fsS -H "Authorization: Bearer $CLRD_TOKEN" "$CLRD$1" "${@:2}"; }

# rows TOPIC KEY: every notification with that external_id, in any state.
rows() {
  api "/api/v1/notifications?topic=$1&state=all&limit=500" |
    jq -c --arg k "$2" '[.notifications[] | select(.external_id == $k)]'
}

# one TOPIC KEY: the only row with that key. Fails on none or more than one,
# so "resolved" can never be satisfied by a second, newer row.
one() {
  local r
  r=$(rows "$1" "$2")
  [ "$(jq length <<<"$r")" = 1 ] || fail "want exactly 1 row for $1/$2, got $r"
  jq -c '.[0]' <<<"$r"
}

# check JSON JQ-EXPR WHAT: fail unless the jq expression is true for JSON.
check() { jq -e "$2" <<<"$1" >/dev/null || fail "$3: $(jq -c . <<<"$1")"; }

# expect TOPIC KEY JQ-EXPR WHAT: the one row with that key matches.
expect() { check "$(one "$1" "$2")" "$3" "$4"; ok "$4"; }

# eventually SECS TOPIC KEY JQ-EXPR WHAT: expect, retried while a tool works.
eventually() {
  local end=$((SECONDS + $1)) r
  while :; do
    r=$(rows "$2" "$3")
    jq -e "length == 1 and (.[0] | $4)" <<<"$r" >/dev/null && { ok "$5"; return; }
    [ "$SECONDS" -lt "$end" ] || fail "$5 (after $1 s): $r"
    sleep 1
  done
}

# snippet FILE TEXT: the fenced code block in docs/integrations/FILE that
# contains TEXT, with the public host and placeholder token swapped for the
# test server's. Cases run the guide's own text, so a wrong guide fails here.
snippet() {
  local out
  out=$(awk -v want="$2" '
    /^ *```/ { if (inb) { if (index(buf, want)) { printf "%s", buf; exit } inb = 0; buf = "" } else inb = 1; next }
    inb { buf = buf $0 "\n" }' "$ROOT/docs/integrations/$1")
  [ -n "$out" ] || fail "no code block in $1 contains: $2"
  # Leading indentation is kept: YAML blocks need it.
  sed -e "s|https://clrd.example.com|$CLRD|g" -e 's|ntfys://|ntfy://|g' \
    -e "s|clrd.example.com|127.0.0.1:$CLRD_PORT|g" -e "s|clrd_\.\.\.|$CLRD_TOKEN|g" <<<"$out"
}

# dock ARGS: docker run on the host network (so the tool reaches clrd on
# 127.0.0.1), capped at 2 CPUs, labelled so run.sh removes it afterwards.
dock() { docker run --rm --network host --cpus 2 --label clrd-it --label "$RUN_ID" "$@"; }

# spawn CMD...: a background process that run.sh kills after the case.
spawn() { "$@" & echo $! >>"$WORK/pids"; }

# free_port: an unused local TCP port.
free_port() { python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1])'; }

# wait_http URL: until it answers at all (any status).
wait_http() {

  for _ in $(seq 120); do curl -s -o /dev/null "$1" && return; sleep 0.5; done
  fail "$1 never answered"
}

# toggle_server: an HTTP server whose /up answers 200 while $WORK/site/up
# exists and 404 once it is removed. Prints its port. For uptime monitors.
toggle_server() {
  local port
  port=$(free_port)
  mkdir -p "$WORK/site" && touch "$WORK/site/up"
  spawn python3 -m http.server -b 127.0.0.1 -d "$WORK/site" "$port" >/dev/null 2>&1
  wait_http "http://127.0.0.1:$port/" >/dev/null
  echo "$port"
}
