# docs/integrations/alertmanager.md, grafana.md and every other ?template=.
# One directory per template under templates/:
#   firing.json    the tool's webhook body for a firing alert
#   resolved.json  the same alert resolved (leave it out if the template cannot resolve)
#   expect.json    {"topic", "external_id", "query" (optional extra URL params),
#                   "row": fields the created notification must have}
for dir in "$HERE"/templates/*/; do
  name=$(basename "$dir")
  want=$(cat "$dir/expect.json")
  topic=$(jq -r .topic <<<"$want")
  key=$(jq -r .external_id <<<"$want")
  url="$CLRD/$topic?template=$name&$(jq -r '.query // ""' <<<"$want")"
  post() { curl -fsS -o /dev/null -H "Authorization: Bearer $CLRD_TOKEN" -H "Content-Type: application/json" --data-binary "@$dir/$1" "$url"; }

  post firing.json
  post firing.json
  expect "$topic" "$key" "$(jq -c .row <<<"$want") as \$w | .state == \"active\" and (. as \$r | \$w | to_entries | all(.value == \$r[.key]))" "$name: firing twice makes one notification with the mapped fields"
  if [ -f "$dir/resolved.json" ]; then
    post resolved.json
    expect "$topic" "$key" '.state == "resolved"' "$name: resolved clears it"
    post resolved.json
    ok "$name: resolved again is not an error"
  fi
done
