# Integration guide tests

`run.sh` replays every guide in [`docs/integrations`](../../docs/integrations/)
against a real clrd, running the real tool in a container where one exists.
CI runs it in [`integrations.yml`](../../.github/workflows/integrations.yml) on
every change to the server, the guides, the SDKs or this directory.

```sh
CLRD_BIN=/path/to/clrd test/integrations/run.sh          # every case
CLRD_BIN=/path/to/clrd test/integrations/run.sh gatus    # one case
```

Needs bash, curl, jq, python3 and docker. Each case gets a fresh server and
database, with `CLRD`, `CLRD_PORT`, `CLRD_TOKEN` (admin) and `WORK` (scratch
dir) set. Helpers are in [`lib.sh`](lib.sh).

## Adding a webhook template (`?template=`)

Add a directory `templates/<name>/` with the tool's real webhook bodies:

- `firing.json`: the body the tool sends for a firing alert.
- `resolved.json`: the same alert resolved. Leave it out if the template cannot resolve.
- `expect.json`: `topic`, `external_id`, optional `query` (extra URL parameters,
  such as `priority=5`), and `row`, the fields the notification must have.

`cases/templates.sh` posts each one to `/<topic>?template=<name>` and checks
the row, that a repeat does not make a second one, and that resolved clears it.

## Adding a guide

Add `cases/<guide>.sh`. Run the guide's own text, not a copy of it:
`snippet <guide>.md '<text unique to the block>'` prints that fenced block with
the public host and `clrd_...` swapped for the test server's. Check results
with `expect` (one row for a key, matching a jq test) or `eventually` (the
same, retried while a tool works). Start tools with `dock` so they are cleaned
up, and background helpers with `spawn`.
