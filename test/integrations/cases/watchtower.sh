# docs/integrations/watchtower.md: real Watchtower, the guide's notification
# URL. shoutrrr's ntfy:// is HTTPS unless told otherwise, so the test server
# adds ?scheme=http. One run-once with nothing to update still sends a report.
url=$(snippet watchtower.md WATCHTOWER_NOTIFICATION_URL | sed 's/^WATCHTOWER_NOTIFICATION_URL=//')
dock -v /var/run/docker.sock:/var/run/docker.sock -e DOCKER_API_VERSION=1.44 \
  -e "WATCHTOWER_NOTIFICATION_URL=$url?scheme=http" containrrr/watchtower:1.7.1 --run-once clrd-it-nothing >/dev/null 2>&1
check "$(api '/api/v1/notifications?topic=docker&state=all')" \
  '.notifications | length == 1 and (.[0].title | startswith("Watchtower updates")) and (.[0].message | contains("Watchtower 1.7"))' "watchtower report"
ok "Watchtower 1.7: run report arrives on docker"
