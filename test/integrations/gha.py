# Checks docs/integrations/github-actions.md against the real action and CI:
#   gha.py WITH_ACTION.yml WITHOUT_ACTION.yml action.yml integrations.yml VARS.json
# Fails unless every `with:` key is an input of sdk/action, required inputs are
# set, and CI runs the action with the guide's exact inputs (url and token
# aside). Prints the "without the action" steps as JSON, with ${{ }} filled in
# from VARS.json, for the case to run.
import json, re, sys, yaml
from pathlib import Path


def load(p):
    return yaml.safe_load(Path(p).read_text())


guide, plain, action, ci, var = sys.argv[1:]
inputs = load(action)["inputs"]
need = {k for k, v in inputs.items() if v.get("required")}
steps = load(guide)["steps"]
uses = [s for s in steps if "uses" in s]
assert len(uses) == 2, uses
ran = [s["with"] for s in load(ci)["jobs"]["guides"]["steps"] if s.get("uses") == "./sdk/action"]
for s in uses:
    assert s["uses"] == "thejdubb02/clrd/sdk/action@main", s["uses"]
    w = s["with"]
    assert set(w) <= set(inputs), f"unknown input {set(w) - set(inputs)}"
    assert need <= set(w), f"missing {need - set(w)}"
    strip = lambda d: {k: str(v) for k, v in d.items() if k not in ("url", "token")}
    assert strip(w) in [strip(r) for r in ran], f"CI does not run this step: {w}"

v = json.loads(Path(var).read_text())
def fill(x):
    return re.sub(r"\$\{\{\s*([\w.-]+)\s*\}\}", lambda m: v[m.group(1)], x)
print(json.dumps([{"if": s["if"], "env": {k: fill(e) for k, e in s["env"].items()}, "run": s["run"]}
                  for s in load(plain)]))
