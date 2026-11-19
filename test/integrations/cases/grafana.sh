# docs/integrations/grafana.md with real Grafana: the guide's provisioning
# file, a default policy sending to it, and an alert rule (a math expression,
# no datasource, checked every 10 s, the minimum) that we flip from firing to normal through the API.
gf=$(free_port)
pw=$(od -An -N12 -tx1 /dev/urandom | tr -d " \n")
mkdir -p "$WORK/prov/alerting"
snippet grafana.md 'contactPoints:' >"$WORK/prov/alerting/contact.yaml"
printf 'apiVersion: 1\npolicies:\n  - orgId: 1\n    receiver: clrd\n    group_wait: 0s\n    group_interval: 1s\n    repeat_interval: 1h\n' >"$WORK/prov/alerting/policy.yaml"
dock -d -v "$WORK/prov:/etc/grafana/provisioning:ro" -e GF_SERVER_HTTP_ADDR=127.0.0.1 -e GF_SERVER_HTTP_PORT="$gf" -e GF_SECURITY_ADMIN_PASSWORD="$pw" grafana/grafana:11.6.0 >/dev/null
for _ in $(seq 120); do curl -fs "http://127.0.0.1:$gf/api/health" >/dev/null && break; sleep 1; done
curl -fs "http://127.0.0.1:$gf/api/health" >/dev/null || fail "Grafana did not start"
gapi() { curl -sS --fail-with-body -u "admin:$pw" -H 'Content-Type: application/json' -H 'X-Disable-Provenance: true' "http://127.0.0.1:$gf$1" "${@:2}"; }
folder=$(gapi /api/folders -d '{"title":"it"}' | jq -r .uid)
rule() {  # rule EXPRESSION: the rule body, firing while EXPRESSION is true
  jq -nc --arg f "$folder" --arg e "$1" '{uid: "high-cpu", title: "High CPU", ruleGroup: "it", folderUID: $f, orgID: 1,
    condition: "A", for: "0s", noDataState: "OK", execErrState: "Error", annotations: {summary: "CPU above 90%"},
    data: [{refId: "A", datasourceUid: "__expr__", relativeTimeRange: {from: 600, to: 0}, model: {refId: "A", type: "math", expression: $e}}]}'
}
group() { gapi "/api/v1/provisioning/folder/$folder/rule-groups/it" -X PUT -d "{\"title\":\"it\",\"interval\":10,\"rules\":[$(rule "$1")]}" >/dev/null; }
group '1 > 0'

# Grafana picks the key (fingerprint); find it from the one row it makes.
end=$((SECONDS + 90))
until key=$(api "/api/v1/notifications?topic=infra&state=all" | jq -er 'select(.notifications | length == 1) | .notifications[0].external_id'); do
  [ "$SECONDS" -lt "$end" ] || fail "Grafana sent nothing in 90 s"; sleep 1
done
expect infra "$key" '.state == "active" and .title == "High CPU" and .message == "CPU above 90%"' "firing: Grafana sends it, keyed by fingerprint"
group '0 > 1'
eventually 90 infra "$key" '.state == "resolved"' "normal again: Grafana resolves the same row"
