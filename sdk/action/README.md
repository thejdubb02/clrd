# clrd GitHub Action

Composite action, needs only `bash`, `curl` and `jq` on the runner. Usage and
the fail-then-resolve pattern: [docs/integrations/github-actions.md](../../docs/integrations/github-actions.md).

Pass `options` as a JSON object to include native send fields such as
`icon`, `attachment`, `actions`, or `reopen`. The dedicated `topic`, `message`,
`title`, `priority`, `id`, and `resolve` inputs still control those fields
and the resolve operation.

Source: [github.com/thejdubb02/clrd](https://github.com/thejdubb02/clrd)
