# clrd for Python

Stdlib only, Python 3.8+.

```sh
pip install clrd
export CLRD_URL=https://clrd.itsjustin.me CLRD_TOKEN=clrd_...
```

```python
import clrd

clrd.send("infra", "Disk 91%", id="disk-build", priority=4)
clrd.resolve("infra", "disk-build")      # clears it on every device

n = clrd.send("deploys", "Ship v2?")
clrd.ack(n["id"])                         # by server id
clrd.snooze(n["id"], "1h")
clrd.list("infra")                        # open notifications

c = clrd.Client("https://clrd.itsjustin.me", "clrd_...")  # explicit config
```

Command line:

```sh
clrd send infra "Disk 91%" --id disk-build -p 4
clrd resolve infra disk-build
clrd list infra
```

Errors raise `clrd.ClrdError` with `.status` and `.code`.

Tests: `python3 -m unittest test_clrd.py`
