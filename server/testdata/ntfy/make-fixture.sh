#!/bin/sh
# Builds user.db, cache.db and attachments/ with the real ntfy binary, so the
# import test reads files ntfy itself wrote. Usage: make-fixture.sh <ntfy binary>
# Pinned: ntfy v2.38.1. Re-run only when moving to a new ntfy version.
set -eu
ADMIN_PASS=adminpass # throwaway, fixture only
NTFY=$(realpath "$1")
cd "$(dirname "$0")"
rm -rf user.db cache.db attachments work
mkdir -p attachments work
PORT=18311
export NTFY_AUTH_FILE="$PWD/user.db"
cat > work/server.yml <<YML
base-url: "http://127.0.0.1:$PORT"
listen-http: "127.0.0.1:$PORT"
cache-file: "$PWD/cache.db"
cache-duration: "8760h"
auth-file: "$PWD/user.db"
auth-default-access: "deny-all"
attachment-cache-dir: "$PWD/attachments"
attachment-expiry-duration: "8760h"
enable-login: true
YML

"$NTFY" serve --config work/server.yml >work/serve.log 2>&1 &
PID=$!
trap 'kill $PID 2>/dev/null' EXIT
for _ in 1 2 3 4 5 6 7 8 9 10; do curl -sf "http://127.0.0.1:$PORT/v1/health" >/dev/null && break; sleep 0.5; done

# Users: an admin, a user with mixed rights, and anonymous rights on one topic.
NTFY_PASSWORD="$ADMIN_PASS" "$NTFY" user --config work/server.yml add --role=admin boss
NTFY_PASSWORD=phonepass "$NTFY" user --config work/server.yml add phone
"$NTFY" access --config work/server.yml phone alerts rw
"$NTFY" access --config work/server.yml phone 'drop*' wo
"$NTFY" access --config work/server.yml phone news ro
"$NTFY" access --config work/server.yml phone secret deny
"$NTFY" access --config work/server.yml phone my_topic ro
"$NTFY" access --config work/server.yml '*' public rw
"$NTFY" access --config work/server.yml '*' 'announce' ro
"$NTFY" token --config work/server.yml add --label "pixel" phone
"$NTFY" token --config work/server.yml add boss
"$NTFY" token --config work/server.yml add --expires=30d --label "web" phone


U="http://127.0.0.1:$PORT"
pub() { curl -sf --user "boss:$ADMIN_PASS" "$@"; }
pub -H "Title: Disk full" -H "Priority: high" -H "Tags: warning,disk" -H "Click: https://example.com/disk" -d "Disk is at 95%" "$U/alerts" >/dev/null
pub -H "X-Sequence-ID: job-1" -d "Job started" "$U/alerts" >/dev/null
pub -H "X-Sequence-ID: job-1" -d "Job finished" "$U/alerts" >/dev/null
pub -H "Filename: report.txt" --data-binary "hello from ntfy" "$U/alerts" >/dev/null
pub -H "Attach: https://example.com/big.zip" -d "External file" "$U/news" >/dev/null
pub -H "Markdown: yes" -d "**bold** news" "$U/news" >/dev/null
pub -H "Delay: 48h" -d "Later" "$U/alerts" >/dev/null
curl -sf -d "Hello anyone" "$U/public" >/dev/null
pub -H "X-Sequence-ID: tmp-1" -d "Temporary" "$U/alerts" >/dev/null
pub -X DELETE "$U/alerts/tmp-1" >/dev/null || echo "no sequence delete"
sleep 1
kill $PID
wait $PID 2>/dev/null || true
trap - EXIT
rm -rf work
rm -f user.db-wal user.db-shm cache.db-wal cache.db-shm
echo "fixture done"
