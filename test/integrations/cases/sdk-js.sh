# sdk/js/README.md: the usage example, as written, importing the package by name.
snippet ../../sdk/js/README.md 'from "clrd-client"' | sed "s|https://clrd.example.com|$CLRD|g" >"$WORK/example.mjs"
dock -e "CLRD_URL=$CLRD" -e "CLRD_TOKEN=$CLRD_TOKEN" \
  -v "$ROOT/sdk/js:/app/node_modules/clrd-client:ro" -v "$WORK/example.mjs:/app/example.mjs:ro" \
  node:22-slim node /app/example.mjs
expect infra disk-build '.state == "resolved" and .priority == 4' "send then resolve by key"
expect deploys "" '.state == "snoozed" and .message == "Ship v2?"' "ack then snooze by id"
