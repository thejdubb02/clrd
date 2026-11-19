# docs/integrations/home-assistant.md. Home Assistant itself is not run: its
# rest_command renders url and payload with Jinja2 and posts them, so this
# renders the guide's own templates with Jinja2 (plus HA's to_json filter) and
# posts the result, for each action in the guide's automation block.
snippet home-assistant.md 'clrd_bearer:' >"$WORK/secrets.yaml"
snippet home-assistant.md 'rest_command:' >"$WORK/config.yaml"
snippet home-assistant.md 'automation:' >"$WORK/automation.yaml"
# The same actions with a key holding "/", which the resolve url must escape.
sed 's/garage-open/garage\/left/' "$WORK/automation.yaml" >"$WORK/automation-slash.yaml"
cat >"$WORK/ha.py" <<'PY'
import json, sys, urllib.request, jinja2, yaml
secrets = yaml.safe_load(open("/w/secrets.yaml"))
yaml.SafeLoader.add_constructor("!secret", lambda l, n: secrets[l.construct_scalar(n)])
env = jinja2.Environment(undefined=jinja2.ChainableUndefined)
env.filters["to_json"] = json.dumps
commands = yaml.safe_load(open("/w/config.yaml"))["rest_command"]
for auto in yaml.safe_load(open(sys.argv[1]))["automation"]:
    for act in auto["actions"]:
        cmd = commands[act["action"].removeprefix("rest_command.")]
        r = lambda t: env.from_string(t).render(**act["data"])
        body = r(cmd["payload"]).encode() if "payload" in cmd else None
        req = urllib.request.Request(r(cmd["url"]), data=body, method=cmd["method"].upper(),
            headers={k: v for k, v in cmd["headers"].items()} | ({"content-type": cmd["content_type"]} if body else {}))
        urllib.request.urlopen(req)
        print("ran", act["action"], act["data"]["id"])
PY
ha() { dock -v "$WORK:/w:ro" python:3.12-slim sh -ec "pip install -q --root-user-action=ignore --disable-pip-version-check jinja2==3.1.6 pyyaml==6.0.2 && python /w/ha.py /w/$1"; }

ha automation.yaml
expect home garage-open '.state == "resolved" and .title == "Garage open" and .message == "Open for 10 minutes" and .priority == 4' "send then resolve, from the guide's automations"
ha automation-slash.yaml
expect home garage/left '.state == "resolved"' "resolve url escapes a key with /"
