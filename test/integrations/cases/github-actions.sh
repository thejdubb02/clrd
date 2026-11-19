# docs/integrations/github-actions.md. The workflow runs the action itself on
# a real runner (integrations.yml); this checks the guide's YAML matches the
# action's inputs and that run, and runs the "without the action" steps.
snippet github-actions.md 'Tell clrd it broke' >"$WORK/with.yml"
snippet github-actions.md 'clrd send failed' >"$WORK/plain.yml"
jq -n --arg t "$CLRD_TOKEN" '{"secrets.CLRD_TOKEN": $t, "github.repository": "octo/app",
  "github.workflow": "build", "github.job": "test", "strategy.job-index": "0", "github.ref_name": "feature/x"}' >"$WORK/vars.json"
steps=$(dock -v "$WORK:/w:ro" -v "$ROOT:/r:ro" python:3.12-slim sh -ec \
  "pip install -q --root-user-action=ignore --disable-pip-version-check pyyaml==6.0.2 &&
   python /r/test/integrations/gha.py /w/with.yml /w/plain.yml /r/sdk/action/action.yml /r/.github/workflows/integrations.yml /w/vars.json")
ok "with the action: inputs exist, required ones set, CI runs these exact steps"

# step IF: run the "if: IF" step as Actions would (bash -e), with its env.
step() {
  local s e
  s=$(jq -c --arg i "$1" '.[] | select(.if == $i)' <<<"$steps")
  [ -n "$s" ] || fail "no step with if: $1"
  mapfile -t e < <(jq -r '.env | to_entries[] | "\(.key)=\(.value)"' <<<"$s")
  env "${e[@]}" \
    GITHUB_SERVER_URL=https://github.com GITHUB_REPOSITORY=octo/app GITHUB_RUN_ID=42 \
    bash -e -c "$(jq -r .run <<<"$s")"
}
key=octo/app-build-test-0-feature/x
step 'failure()'
expect ci "$key" '.state == "active" and .priority == 4 and .message == "https://github.com/octo/app/actions/runs/42"' "without the action: failure sends"
step 'success()'
expect ci "$key" '.state == "resolved"' "without the action: success resolves (slashes in the key)"
step 'success()'
ok "without the action: resolve with nothing open is fine"
