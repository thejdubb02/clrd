# docs/integrations/apprise.md with the real Apprise CLI.
cmd=$(snippet apprise.md 'apprise -t')
dock python:3.12-slim sh -ec "pip install -q --root-user-action=ignore --disable-pip-version-check apprise==1.9.3 && $cmd"
expect infra "" '.state == "active" and .title == "Disk 91%" and .message == "/var on build is 91% full"' "apprise ntfy:// URL with ?auth=token"
