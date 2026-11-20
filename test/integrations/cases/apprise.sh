# docs/integrations/apprise.md with the real Apprise CLI.
cmd=$(snippet apprise.md 'apprise -t')
dock python:3.12-slim sh -ec "pip install -q --root-user-action=ignore --disable-pip-version-check apprise==1.9.3 && $cmd"
expect infra "" '.state == "active" and .title == "Disk 91%" and .message == "/var on build is 91% full"' "apprise ntfy:// URL with ?auth=token"

# The guide's equivalent URL form, plus ?priority=high.
eq=$(grep -o 'ntfys://clrd.itsjustin.me/infra?token=clrd_\.\.\.' "$ROOT/docs/integrations/apprise.md" | sed -e "s|ntfys://clrd.itsjustin.me|ntfy://127.0.0.1:$CLRD_PORT|" -e "s|clrd_\.\.\.|$CLRD_TOKEN|")
[ -n "$eq" ] || fail "the equivalent ?token= URL is gone from the guide"
dock python:3.12-slim sh -ec "pip install -q --root-user-action=ignore --disable-pip-version-check apprise==1.9.3 && apprise -t Equivalent -b body '$eq&priority=high'"
check "$(rows infra "")" 'map(select(.title == "Equivalent")) | length == 1' "?token= form"
ok "?token= form"
# Apprise sends priority as X-Priority next to a JSON body (#130).
check "$(rows infra "")" 'map(select(.title == "Equivalent")) | .[0].priority == 4' "?priority=high arrives as priority 4"
ok "?priority=high arrives"
