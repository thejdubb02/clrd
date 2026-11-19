# docs/integrations/ntfy-clients.md with the real ntfy CLI.
# The CLI adds https:// to a bare host; the test server is plain HTTP.
cli=$(snippet ntfy-clients.md 'ntfy publish' | sed "s| 127.0.0.1:| http://127.0.0.1:|")
pub=$(grep -A1 'ntfy publish' <<<"$cli")
sub=$(grep 'ntfy subscribe' <<<"$cli" | sed 's/ntfy subscribe/ntfy subscribe --poll/')
ntfy() { dock --entrypoint sh binwiederhier/ntfy:v2.14.0 -ec "$1"; }

ntfy "$pub" >/dev/null
expect infra "" '.state == "active" and .title == "Disk 91%" and .priority == 4 and .message == "/var on build is 91% full"' "ntfy publish with --token"
got=$(ntfy "$sub")
check "$got" '.event == "message" and .title == "Disk 91%"' "ntfy subscribe --poll"
ok "ntfy subscribe --poll reads it back"

# Auto-resolve: send with X-Id, then resolve by key.
resolve=$(snippet ntfy-clients.md 'X-Id: disk-build')
sh -e -c "$(sed -n 1p <<<"$resolve")" >/dev/null
expect infra disk-build '.state == "active" and .message == "Disk 91%"' "curl with X-Id"
sh -e -c "$(sed -n 2p <<<"$resolve")" >/dev/null
expect infra disk-build '.state == "resolved"' "resolve by key"
