# docs/integrations/gatus.md with real Gatus: the guide's config, pointed at a
# local site that goes down and comes back, checked every second.
site=$(toggle_server)
web=$(free_port)
snippet gatus.md 'alerting:' |
  sed -e "s|url: https://example.com|url: http://127.0.0.1:$site/up\n    interval: 1s|" >"$WORK/gatus.yaml"
printf 'web:\n  address: 127.0.0.1\n  port: %s\n' "$web" >>"$WORK/gatus.yaml"
grep -q "interval: 1s" "$WORK/gatus.yaml" || fail "the endpoint url in the guide changed"
dock -d -v "$WORK/gatus.yaml:/config/config.yaml:ro" twinproduction/gatus:v5.17.0 >/dev/null

rm "$WORK/site/up"
eventually 60 infra gatus--website '.state == "active" and .title == "website is down" and .priority == 4 and (.message | startswith("health check failed"))' "down: Gatus sends the alert"
touch "$WORK/site/up"
eventually 60 infra gatus--website '.state == "resolved"' "back up: Gatus resolves it"
