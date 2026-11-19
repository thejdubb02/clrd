# docs/integrations/alertmanager.md with real Alertmanager: the guide's config
# with the grouping waits shortened, and an alert that fires then resolves.
am=$(free_port)
snippet alertmanager.md 'receivers:' |
  sed "s|  receiver: clrd .*|  receiver: clrd\n  group_wait: 0s\n  group_interval: 1s|" >"$WORK/am.yml"
grep -q "group_interval" "$WORK/am.yml" || fail "the route in the guide changed"
dock -d -v "$WORK/am.yml:/etc/am.yml:ro" prom/alertmanager:v0.28.1 --config.file=/etc/am.yml \
  --web.listen-address=127.0.0.1:"$am" --cluster.listen-address= --storage.path=/tmp/am >/dev/null
wait_http "http://127.0.0.1:$am/-/ready"

alert() {  # alert ENDS_AT
  curl -fsS -H 'Content-Type: application/json' "http://127.0.0.1:$am/api/v2/alerts" -d "$(jq -nc --arg e "$1" \
    '[{labels: {alertname: "DiskFull", instance: "build"}, annotations: {summary: "/var on build is 91% full"}, endsAt: $e, generatorURL: "http://prometheus.example.com/graph"}]')"
}
alert "$(date -u -d '+1 hour' +%FT%TZ)"
fp=$(curl -fsS "http://127.0.0.1:$am/api/v2/alerts" | jq -er '.[0].fingerprint') || fail "Alertmanager has no alert"
eventually 60 infra "$fp" '.state == "active" and .title == "DiskFull" and .message == "/var on build is 91% full" and .click == "http://prometheus.example.com/graph"' "firing: one row keyed by fingerprint"
alert "$(date -u +%FT%TZ)"
eventually 60 infra "$fp" '.state == "resolved"' "resolved: the same row clears"
