# GitHub Actions

Native API only. Works today.

Add a repository secret `CLRD_TOKEN` (a token with `publish` scope), then use
the composite action in [`sdk/action`](../../sdk/action). It needs `bash`,
`curl` and `jq` on the runner (all present on GitHub-hosted runners).

## Send on failure, resolve on the next success

The key is per workflow, job, matrix leg and branch, so a fixed build clears
its own alert from every device, and one passing matrix leg cannot clear
another leg's failure. `strategy.job-index` is the leg's position in the
matrix (0 without one): if you reorder the matrix, an old alert can end up
under another leg's key, so resolve or dismiss it by hand once. These steps go
at the end of the job:

```yaml
    steps:
      # ... your build and test steps ...

      - name: Tell clrd it broke
        if: failure()
        uses: thejdubb02/clrd/sdk/action@main
        with:
          token: ${{ secrets.CLRD_TOKEN }}
          topic: ci
          id: ${{ github.event.repository.name }}-${{ github.workflow }}-${{ github.job }}-${{ strategy.job-index }}-${{ github.ref_name }}
          title: ${{ github.workflow }} failed on ${{ github.ref_name }}
          message: ${{ github.server_url }}/${{ github.repository }}/actions/runs/${{ github.run_id }}
          priority: 4

      - name: Clear the clrd alert
        if: success()
        uses: thejdubb02/clrd/sdk/action@main
        with:
          token: ${{ secrets.CLRD_TOKEN }}
          topic: ci
          id: ${{ github.event.repository.name }}-${{ github.workflow }}-${{ github.job }}-${{ strategy.job-index }}-${{ github.ref_name }}
          resolve: true
```

Resolving when nothing is open is a no-op (the action treats 404 as fine).
Keys are URL-encoded in the resolve path, so spaces and slashes (`feature/x`) are fine.
Pin `@main` to a tag or commit once releases exist.

Inputs: `url` (default `https://clrd.itsjustin.me`), `token`, `topic`, `id`,
`message`, `title`, `priority` (1 to 5, default 3), `resolve` (`true` to
resolve instead of send).

## Without the action

```yaml
      - if: failure()
        env:
          CLRD_TOKEN: ${{ secrets.CLRD_TOKEN }}
          KEY: ${{ github.repository }}-${{ github.workflow }}-${{ github.job }}-${{ strategy.job-index }}-${{ github.ref_name }}
        run: |
          jq -n --arg key "$KEY" --arg msg "$GITHUB_SERVER_URL/$GITHUB_REPOSITORY/actions/runs/$GITHUB_RUN_ID" \
            '{topic:"ci", external_id:$key, title:"Build failed", message:$msg, priority:4}' |
          code=$(curl -sS -o /dev/null -w '%{http_code}' -X POST https://clrd.itsjustin.me/api/v1/notifications \
            -H "Authorization: Bearer $CLRD_TOKEN" -H "Content-Type: application/json" --data @-)
          case "$code" in 2??) ;; *) echo "clrd send failed: $code"; exit 1 ;; esac
      - if: success()
        env:
          CLRD_TOKEN: ${{ secrets.CLRD_TOKEN }}
          KEY: ${{ github.repository }}-${{ github.workflow }}-${{ github.job }}-${{ strategy.job-index }}-${{ github.ref_name }}
        run: |
          code=$(curl -sS -o /dev/null -w '%{http_code}' -X POST -H "Authorization: Bearer $CLRD_TOKEN" \
            "https://clrd.itsjustin.me/ci/$(jq -rn --arg v "$KEY" '$v|@uri')/resolve")
          case "$code" in 2??|404) ;; *) echo "clrd resolve failed: $code"; exit 1 ;; esac
```
