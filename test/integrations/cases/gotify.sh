# docs/integrations/gotify.md: an app token from the guide's API call, then
# the guide's curl and Apprise lines, and the real gotify CLI.
app=$(sh -e -c "$(snippet gotify.md '/api/v1/tokens')" | jq -r .token)
[ "${app#clrd_}" != "$app" ] || fail "the guide's token call returned no token"

CLRD_TOKEN=$app sh -e -c "$(CLRD_TOKEN=$app snippet gotify.md '/message?token=')" >/dev/null
check "$(rows tv "")" 'map(select(.title == "Backup done")) | .[0] | .message == "nightly backup finished" and .priority == 3' "curl -F with ?token="
ok "curl -F with ?token= (priority 5 is 3)"

cmd=$(CLRD_TOKEN=$app snippet gotify.md 'apprise -t' | sed 's|gotifys://|gotify://|')
dock python:3.12-slim sh -ec "pip install -q --root-user-action=ignore --disable-pip-version-check apprise==1.9.3 && $cmd"
check "$(rows tv "")" 'map(select(.title == "Backup done")) | length == 2' "apprise gotify:// URL"
ok "apprise gotify:// URL"

# The gotify CLI 2.4 reads url and token from ./cli.json.
mkdir -p "$WORK/gotify"
printf '{"token":"%s","url":"%s","defaultPriority":5}' "$app" "$CLRD" >"$WORK/gotify/cli.json"
dock -v "$WORK/gotify:/w" -w /w alpine:3.20 sh -ec "wget -qO /g https://github.com/gotify/cli/releases/download/v2.4.0/gotify-cli-linux-amd64 && chmod +x /g && /g push -t 'From the CLI' -p 9 'gotify cli push'"
check "$(rows tv "")" 'map(select(.title == "From the CLI")) | .[0] | .message == "gotify cli push" and .priority == 4' "gotify cli push"
ok "gotify cli push (priority 9 is 4)"

# A wrong key is Gotify's 401, and the topic "message" still takes ntfy posts.
code=$(curl -s -o /dev/null -w '%{http_code}' "$CLRD/message" -H "X-Gotify-Key: nope" -F message=x)
[ "$code" = 401 ] || fail "wrong key: $code"
ok "wrong key is 401"
api /message -d "plain ntfy" >/dev/null
check "$(rows message "")" 'map(select(.message == "plain ntfy")) | length == 1' "POST /message without a key is ntfy"
ok "POST /message without a key is ntfy"
