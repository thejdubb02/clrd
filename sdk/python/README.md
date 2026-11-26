# clrd for Python

Stdlib only, Python 3.8+.

```sh
# not on PyPI yet; install from GitHub
pip install "git+https://github.com/thejdubb02/clrd#subdirectory=sdk/python"
export CLRD_URL=https://clrd.example.com CLRD_TOKEN=clrd_...
```

```python
import clrd

clrd.send("infra", "Disk 91%", id="disk-build", priority=4)
clrd.resolve("infra", "disk-build")      # clears it on every device

n = clrd.send("deploys", "Ship v2?")
clrd.ack(n["id"])                         # by server id
clrd.snooze(n["id"], "1h")
clrd.mark(n["id"], "actioned", note="filed #5")  # others see it; it clears nothing
clrd.list("infra")                        # open notifications

c = clrd.Client("https://clrd.example.com", "clrd_...")  # explicit config
```

Command line:

```sh
clrd send infra "Disk 91%" --id disk-build -p 4
clrd resolve infra disk-build
clrd list infra
```

Errors raise `clrd.ClrdError` with `.status` and `.code`.

Tests: `python3 -m unittest test_clrd.py`

`send` accepts keyword options including `icon`, an external `attachment`
with `url` and optional `name`, and copy actions such as
`{"label": "Copy", "copy": "build-42"}`. Reuse `id` on the same topic to
update an open notification. Pass `reopen=True` to return an acknowledged
notification to active when updating it. Delivery options include
`snooze_until` (an RFC3339 timestamp), `expires`, `repeat`, `repeat_times`,
`live`, and `devices`.
For the Python CLI, pass these as a JSON object using `send --options`.
Keep the topic and message positional and use `--title`, `--priority`, or `--id`
for those fields.

Source: [github.com/thejdubb02/clrd](https://github.com/thejdubb02/clrd)
