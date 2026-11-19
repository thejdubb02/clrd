# docs/integrations/custom-webhooks.md: both token forms, a saved secret, and
# the guide's own template saved, then fed events.
sh -e -c "$(snippet custom-webhooks.md 'templates/my-app')" >/dev/null 2>&1
ok "the guide's template saves"
my() {
  curl -sS -o /dev/null -w '%{http_code}' -H "X-Clrd-Secret: $1" -H 'Content-Type: application/json' \
    -H "Authorization: Bearer $CLRD_TOKEN" "$CLRD/infra?template=my-app" --data-binary "$2"
}
ev='{"events":[{"service":"db","summary":"replica lag","detail":"40 s behind","id":"db-lag","state":"alerting","level":"critical"}]}'
[ "$(my wrong "$ev")" = 401 ] || fail "wrong secret should get 401"
ok "wrong secret: 401"
[ "$(my 'optional shared secret' "$ev")" = 200 ] || fail "my-app send"
expect infra db-lag '.state == "active" and .title == "db: replica lag" and .message == "40 s behind" and .priority == 5' "my-app: each, title, message, id, priority"
[ "$(my 'optional shared secret' "${ev/alerting/ok}")" = 200 ] || fail "my-app resolve"
expect infra db-lag '.state == "resolved"' "my-app: resolve"

# Sending the token: ?auth= from the guide's recipe, and Basic auth in the URL.
recipe=$(grep -o 'echo -n "Bearer clrd_\.\.\." | base64 | tr -d .=.' "$ROOT/docs/integrations/custom-webhooks.md")
auth=$(sh -c "${recipe//clrd_.../$CLRD_TOKEN}")
basic=$(grep -o 'https://anything:clrd_\.\.\.@clrd.itsjustin.me/infra?template=github' "$ROOT/docs/integrations/custom-webhooks.md")
basic=${basic/clrd_.../$CLRD_TOKEN}; basic=${basic/https:\/\/anything:/http://anything:}; basic=${basic/clrd.itsjustin.me/127.0.0.1:$CLRD_PORT}
gh=$ROOT/server/testdata/webhooks/github.json
for url in "$CLRD/infra?template=github&auth=$auth" "$basic"; do
  curl -fsS -H 'X-GitHub-Event: issues' "$url" --data-binary "@$gh" >/dev/null || fail "token form: $url"
done
ok "?auth= recipe and Basic auth URL both publish"

# Saving GitHub's secret makes clrd check the signature: unsigned gets 401.
sh -e -c "$(snippet custom-webhooks.md 'the secret you gave GitHub')" >/dev/null 2>&1
code=$(curl -sS -o /dev/null -w '%{http_code}' -H 'X-GitHub-Event: issues' "$basic" --data-binary "@$gh")
[ "$code" = 401 ] || fail "unsigned GitHub webhook after saving a secret: $code"
ok "saved secret: unsigned GitHub webhook gets 401"
