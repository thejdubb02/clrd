# The guides for ?template= services: post a real webhook body (the server's
# test fixtures, from each service's docs) to the URL the guide gives, with the
# token sent the way that guide says, then the event that resolves it.
#   name  title  "from|to" (swap in the fixture to get the resolving event)
cases='
github|Codertocat/Hello-World: issues opened|"opened"|"closed"
sentry|Error generated with event_id: 495d375a-1df6-45c0-9890-34dae8e1b6a4(Priority: HIGH)|"created"|"resolved"
stripe|setup_intent.created||
jira|JRA-20002: I feel the need for speed|"jira:issue_updated"|"jira:issue_deleted"
healthchecks|nightly-backup is down|"down"|"up"
netdata|disk_space_usage on disk_space._|"critical"|"clear"
jellyfin|The Matrix||
arr|Sonarr: Health|"Health"|"HealthRestored"'
# The custom-webhooks.md recipe for ?auth=.
auth=$(echo -n "Bearer $CLRD_TOKEN" | base64 -w0 | tr -d '=')
while IFS='|' read -r name title from to; do
  [ -n "$name" ] || continue
  url=$(grep -ho "https://clrd.example.com/[a-z]*?template=${name}[^ \`)]*" "$ROOT/docs/integrations/$name.md" | head -1)
  [ -n "$url" ] || fail "$name.md has no ?template=$name URL"
  url=${url/https:\/\/clrd.example.com/$CLRD}
  url=${url/<token>/$auth}
  topic=${url#"$CLRD"/}; topic=${topic%%\?*}
  # Without &auth= the guide has the service send the token itself.
  hdr=(); [[ $url == *auth=* ]] || hdr=(-H "Authorization: Bearer $CLRD_TOKEN")
  [ "$name" = github ] && hdr+=(-H "X-GitHub-Event: issues")
  body=$(cat "$ROOT/server/testdata/webhooks/$name.json")
  post() { curl -fsS "${hdr[@]}" -H 'Content-Type: application/json' "$url" --data-binary "$1"; }
  [ "$(post "$body")" = '{"published":1,"resolved":0}' ] || fail "$name: send"
  check "$(api "/api/v1/notifications?topic=$topic&state=all")" \
    "[.notifications[] | select(.title == $(jq -n --arg t "$title" '$t'))] | length == 1" "$name: row"
  if [ -n "$from" ]; then
    [ "$(post "${body/"$from"/"$to"}")" = '{"published":0,"resolved":1}' ] || fail "$name: resolve"
  fi
  ok "$name.md: its URL sends${from:+ and resolves}"
done <<<"$cases"

# proxmox.md: the guide's body template, filled in as Proxmox VE would.
fx=$ROOT/server/testdata/webhooks
pve=$(snippet proxmox.md 'escape title')
for f in title message severity; do
  v=$(jq -r --arg f "$f" '.[$f] | tojson | .[1:-1]' "$fx/proxmox.json")
  pve=${pve//"{{ escape $f }}"/$v}; pve=${pve//"{{ $f }}"/$v}
done
curl -fsS -H "Authorization: Bearer $CLRD_TOKEN" -H 'Content-Type: application/json' "$CLRD/pve?template=proxmox" --data-binary "$pve" >/dev/null
expect pve "" '.title == "vzdump backup status (pve1): backup failed" and .priority == 4' "proxmox.md: its body template sends"

# zabbix.md: the guide's media type script, run with Zabbix's parameters, and
# what it posts sent on. Then the recovery (value 0) resolves it.
zbx() {
  jq -c --arg url "$CLRD/zabbix?template=zabbix" --arg token "$CLRD_TOKEN" --arg v "$1" \
    '. + {url: $url, token: $token, value: $v}' "$fx/zabbix.json" >"$WORK/zbx.json"
  snippet zabbix.md 'new HttpRequest' >"$WORK/zbx.js"
  dock -v "$WORK:/w:ro" node:22-slim node -e '
    const fs = require("fs"), sent = {headers: []};
    globalThis.HttpRequest = function () {
      this.addHeader = (h) => sent.headers.push(h);
      this.post = (url, body) => { sent.url = url; sent.body = body; return ""; };
      this.getStatus = () => 200;
    };
    const value = fs.readFileSync("/w/zbx.json", "utf8");
    new Function("value", fs.readFileSync("/w/zbx.js", "utf8"))(value);
    console.log(JSON.stringify(sent));' >"$WORK/sent.json"
  mapfile -t h < <(jq -r '.headers[] | "-H", .' "$WORK/sent.json")
  curl -fsS "${h[@]}" "$(jq -r .url "$WORK/sent.json")" --data-binary "$(jq -r .body "$WORK/sent.json")"
}
[ "$(zbx 1)" = '{"published":1,"resolved":0}' ] || fail "zabbix: problem"
[ "$(zbx 0)" = '{"published":0,"resolved":1}' ] || fail "zabbix: recovery"
expect zabbix zabbix-48213 '.state == "resolved" and .title == "Problem: High CPU utilization on web1" and .priority == 4' "zabbix.md: its script sends, recovery resolves"
