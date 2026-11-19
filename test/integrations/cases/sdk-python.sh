# sdk/python/README.md: the CLI example, then the library example, as written.
readme() { snippet ../../sdk/python/README.md "$1" | sed "s|https://clrd.example.com|$CLRD|g"; }
readme 'clrd send infra' >"$WORK/cli.sh"
readme 'clrd.send(' >"$WORK/example.py"
dock -e "CLRD_URL=$CLRD" -e "CLRD_TOKEN=$CLRD_TOKEN" -e PYTHONDONTWRITEBYTECODE=1 \
  -v "$ROOT/sdk/python:/src:ro" -v "$WORK:/w:ro" python:3.12-slim sh -ec '
  cp -r /src /tmp/sdk && pip install -q --root-user-action=ignore --disable-pip-version-check /tmp/sdk
  sh -e /w/cli.sh && python /w/example.py'
# CLI and library each sent disk-build and resolved it: two rows, both resolved.
check "$(rows infra disk-build)" 'length == 2 and all(.state == "resolved" and .priority == 4)' "send then resolve, twice"
ok "CLI and library: send then resolve by key"
expect deploys "" '.state == "snoozed" and .message == "Ship v2?"' "library: ack then snooze by id"
