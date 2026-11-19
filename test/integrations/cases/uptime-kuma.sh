# docs/integrations/uptime-kuma.md with real Uptime Kuma 1.23: a Webhook
# notification set up from the guide's fields, on an HTTP monitor of a local
# site that goes down and comes back (Kuma checks at most every 20 s).
site=$(toggle_server)
kuma=$(free_port)
# The guide's bullet fields, then its Liquid body.
field() { grep -m1 -- "- $1:" "$ROOT/docs/integrations/uptime-kuma.md" | sed -e 's/^[^`]*`//' -e 's/`$//' \
  -e "s|https://clrd.itsjustin.me|$CLRD|" -e "s|clrd_\.\.\.|$CLRD_TOKEN|"; }
jq -n --arg url "$(field 'Post URL')" --arg headers "$(field 'Additional Headers')" \
  --arg body "$(snippet uptime-kuma.md 'monitorJSON.name')" --arg site "http://127.0.0.1:$site/up" \
  '{url: $url, headers: $headers, body: $body, site: $site}' >"$WORK/kuma.json"
cat >"$WORK/kuma.py" <<'PY'
import json
from uptime_kuma_api import UptimeKumaApi, NotificationType, MonitorType
c = json.load(open("/w/kuma.json"))
api = UptimeKumaApi("http://127.0.0.1:" + __import__("os").environ["KUMA"], timeout=30)
api.setup("admin", "admin12345")
api.login("admin", "admin12345")
n = api.add_notification(name="clrd", type=NotificationType.WEBHOOK, isDefault=False, webhookURL=c["url"],
                         webhookContentType="custom", webhookCustomBody=c["body"], webhookAdditionalHeaders=c["headers"])
m = api.add_monitor(type=MonitorType.HTTP, name="website", url=c["site"], interval=20, retryInterval=20,
                    maxretries=0, notificationIDList=[n["id"]])
print(m["monitorID"])
PY
dock -d -e UPTIME_KUMA_PORT="$kuma" -e UPTIME_KUMA_HOST=127.0.0.1 louislam/uptime-kuma:1.23.16 >/dev/null
for _ in $(seq 120); do curl -fs "http://127.0.0.1:$kuma/" >/dev/null && break; sleep 1; done
id=$(dock -e KUMA="$kuma" -v "$WORK:/w:ro" python:3.12-slim sh -ec \
  "pip install -q --root-user-action=ignore --disable-pip-version-check uptime-kuma-api==1.2.1 && python /w/kuma.py" | tail -1)
[ -n "$id" ] || fail "could not set up Kuma"
ok "Kuma set up from the guide, monitor $id"

rm "$WORK/site/up"
eventually 90 infra "kuma-$id" '.state == "active" and .title == "website is down" and .priority == 4' "down: Kuma sends the alert"
touch "$WORK/site/up"
eventually 90 infra "kuma-$id" '.state == "resolved"' "back up: Kuma resolves it"
