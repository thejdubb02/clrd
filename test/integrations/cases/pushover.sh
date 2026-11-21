# docs/integrations/pushover.md: an app key from the guide's call, then the
# guide's curl line with that key.
out=$(sh -e -c "$(snippet pushover.md '/api/v1/pushover/apps')")
key=$(jq -r .token <<<"$out")
user=$(jq -r .user <<<"$out")
if [ "${#key}" != 30 ] || [ "${#user}" != 30 ]; then
  fail "the guide's app call returned no Pushover-shaped key: $out"
fi
ok "the guide's app call makes a 30-character key"

cmd=$(snippet pushover.md '/1/messages.json' | sed -e "s|token=\.\.\.|token=$key|" -e "s|user=\.\.\.|user=$user|")
reply=$(sh -e -c "$cmd")
check "$reply" '.status == 1 and (.request | length) > 0' "the guide's curl reply"
check "$(rows backups "")" 'map(select(.title == "Backup done")) | .[0] | .message == "nightly backup finished" and .priority == 3 and .format == "text"' "the guide's curl line"
ok "the guide's curl line sends (priority 0 is 3)"

# JSON, as some senders post it, with an emergency priority.
reply=$(curl -fsS "$CLRD/1/messages.json" -H 'Content-Type: application/json' \
  -d "{\"token\":\"$key\",\"user\":\"$user\",\"message\":\"disk full\",\"priority\":2,\"retry\":60,\"expire\":600}")
check "$reply" '(.receipt | length) == 30' "emergency receipt"
check "$(rows backups "")" 'map(select(.message == "disk full")) | .[0] | .priority == 5 and .title == "Backups"' "emergency JSON"
ok "JSON priority 2 is 5, titled with the app name, with a receipt"

# A wrong key is Pushover's 400 naming the token; the key fails elsewhere.
code=$(curl -s -o "$WORK/bad" -w '%{http_code}' "$CLRD/1/messages.json" --form-string token=nope --form-string user=u --form-string message=x)
[ "$code" = 400 ] || fail "wrong key: $code"
check "$(cat "$WORK/bad")" '.token == "invalid" and .status == 0' "wrong key"
ok "wrong key is 400 token invalid"
code=$(curl -s -o /dev/null -w '%{http_code}' "$CLRD/api/v1/notifications" -H "Authorization: Bearer $key")
[ "$code" = 401 ] || fail "app key on the native API: $code"
ok "app key is 401 on the native API"
