# docs/integrations/curl.md
eval "$(snippet curl.md 'export CLRD_TOKEN')"

sh -e -c "$(snippet curl.md 'Content-Type: application/json')" >/dev/null
expect infra disk-build '.state == "active" and .title == "Disk 91%" and .priority == 4' "JSON send"

sh -e -c "$(snippet curl.md 'X-Id: disk-build')" >/dev/null
expect infra disk-build '.state == "active" and .message == "/var on build is 91% full"' "ntfy-style send updates the same row"

sh -e -c "$(snippet curl.md '/infra/disk-build/resolve')" >/dev/null
expect infra disk-build '.state == "resolved"' "resolve by key"
if sh -c "$(snippet curl.md '/infra/disk-build/resolve')" 2>/dev/null; then fail "second resolve should 404 under curl -f"; fi
ok "resolving nothing open exits non-zero"

# The cron pattern, with df faked so the disk is full, then not.
mkdir -p "$WORK/bin"
printf '#!/bin/sh\necho Use%%; echo " $(cat %s/use)%%"\n' "$WORK" >"$WORK/bin/df"
chmod +x "$WORK/bin/df"
cron=$(snippet curl.md 'df --output=pcent')
key=disk-$(hostname)
echo 95 >"$WORK/use"
PATH=$WORK/bin:$PATH sh -e -c "$cron"
expect infra "$key" '.state == "active" and .title == "Disk 95%" and .priority == 4' "cron: full disk sends"
echo 40 >"$WORK/use"
PATH=$WORK/bin:$PATH sh -e -c "$cron"
expect infra "$key" '.state == "resolved"' "cron: recovery resolves"
PATH=$WORK/bin:$PATH sh -e -c "$cron"
ok "cron: nothing open is fine"
