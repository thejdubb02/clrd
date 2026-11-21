# Webhooks from any service

`POST /{topic}?template=<name>` takes a service's own webhook JSON and turns it
into notifications. clrd ships templates for common services (below), and a
topic owner can save their own for anything else.

## Sending the token

Most services cannot add an `Authorization` header to a webhook, so put the
token in the URL. Either works:

- `?auth=` plus the base64 of `Bearer clrd_...`:
  `echo -n "Bearer clrd_..." | base64 | tr -d '='`
- Basic auth in the URL: `https://anything:clrd_...@clrd.example.com/infra?template=github`

Use a token scoped to `publish` on that one topic.

## Built-in templates

| `?template=` | Service | Resolves itself | Checks |
|---|---|---|---|
| `alertmanager` | [Alertmanager](alertmanager.md) | yes | shared secret |
| `grafana` | [Grafana](grafana.md) | yes | shared secret |
| `github` | [GitHub](github.md) | issue or PR closed | `X-Hub-Signature-256` |
| `sentry` | [Sentry](sentry.md) | issue or alert resolved | `Sentry-Hook-Signature` |
| `stripe` | [Stripe](stripe.md) | no | `Stripe-Signature` |
| `jira` | [Jira](jira.md) | issue done or deleted | `X-Hub-Signature` |
| `healthchecks` | [Healthchecks.io](healthchecks.md) | check up | shared secret |
| `proxmox` | [Proxmox VE](proxmox.md) | no | shared secret |
| `zabbix` | [Zabbix](zabbix.md) | problem recovered | shared secret |
| `netdata` | [Netdata](netdata.md) | alert clear | shared secret |
| `jellyfin` | [Jellyfin](jellyfin.md) | no | shared secret |
| `arr` | [Sonarr, Radarr, Lidarr, Readarr](arr.md) | health check passes | shared secret |

[Watchtower](watchtower.md) needs no template: it speaks ntfy.

## Secrets

A built-in works with no setup. To check that a request really came from the
service, save its secret for the topic (scope `publish` on the topic):

```sh
curl -X PUT https://clrd.example.com/api/v1/topics/infra/templates/github \
  -H "Authorization: Bearer clrd_..." -d '{"secret":"the secret you gave GitHub"}'
```

GitHub, Jira, Sentry and Stripe sign each request with the secret, and clrd
checks the signature. Every other service sends the secret back as is, in an
`X-Clrd-Secret` header or `?secret=` on the URL. A request that fails the check
gets 401 and publishes nothing. The API never returns a saved secret.

## In Settings

The web app's Settings page has a Webhook templates section for any device
whose token can publish. It lists the saved templates on each topic you can
publish to, and adds, edits or deletes them. Leave the secret empty on Edit to
keep the saved one. Try it on a sample runs the form's fields, saved or not,
over a pasted JSON post and shows the notifications it would make, without
publishing anything.

## Your own template

Save a template under any name (letters, digits, `_` and `-`), then send to
`?template=<that name>`:

```sh
curl -X PUT https://clrd.example.com/api/v1/topics/infra/templates/my-app \
  -H "Authorization: Bearer clrd_..." -d '{
    "each": "events",
    "title": "{{get . \"service\"}}: {{get . \"summary\" \"text\"}}",
    "message": "{{get . \"detail\"}}",
    "id": "{{get . \"id\"}}",
    "resolve": "{{eq (get . \"state\") \"ok\"}}",
    "priority": "{{if eq (get . \"level\") \"critical\"}}urgent{{end}}",
    "secret": "optional shared secret"
  }'
```

Every field is a [Go template](https://pkg.go.dev/text/template) run over the
JSON body:

| Field | Renders to |
|---|---|
| `each` | not a template: a dotted path to a list. One notification per item, and the templates run over the item. Empty means the whole body is one notification. |
| `title`, `message`, `click` | the notification's fields |
| `priority` | `1` to `5`, or `min`, `low`, `default`, `high`, `urgent`. Empty is 3. `&priority=` on the URL wins. |
| `tags` | a comma separated list, added to any `&tags=` on the URL |
| `id` | the `external_id`: the same id updates one notification instead of stacking |
| `resolve` | `true` resolves the notification with that `id` (nothing happens if it is already gone) |

The message is Markdown. To show it exactly as sent, add `&markdown=false` to
the URL (or send an `X-Markdown: no` header), as on any ntfy style publish.

Functions:

- `get . "a.b" "c.0.d"`: the first path that is set, as text. Paths use dots,
  and numbers index lists. A missing key is empty, never an error, so one
  template can handle every event a service sends.
- `header "X-Event"`: a request header. Headers whose name contains auth,
  cookie, secret, signature, token or key always read as empty.
- `contains`, `lower`, `split`, plus Go's `eq`, `ne`, `and`, `or`, `not`,
  `index`, `if` and `with`. `range`, `define`, `template` and `printf` are
  refused: use `each` for lists.

A template that does not parse is refused when you save it. Saving under a
built-in's name keeps the built-in and overrides only the fields you give, so
`{"secret": "..."}` alone just sets the secret.

`GET /api/v1/topics/{topic}/templates` lists the saved templates (with
`has_secret` in place of the secret) and the built-in names.
`DELETE /api/v1/topics/{topic}/templates/{name}` removes one.

## Try a template first

`POST /api/v1/topics/{topic}/templates/{name}/preview` runs a template over a
sample payload and returns the notifications a real post would make, without
publishing or storing anything. The secret and signature are not checked.

```sh
curl -H "Authorization: Bearer $TOKEN" -d '{
  "payload": {"items": [{"id": "a", "name": "Disk full"}]},
  "template": {"each": "items", "title": "{{get . \"name\"}}", "id": "{{get . \"id\"}}"}
}' https://clrd.example.com/api/v1/topics/ci/templates/my-app/preview
```

Leave out `template` to try the saved or built-in template of that name.
`headers` stands in for the service's request headers, for templates that
use `header`, such as GitHub's `X-GitHub-Event`. `?priority=` and `?tags=`
work as on a real post.

Agents do the same with the MCP tools `templates`, `set_template`,
`delete_template` and `preview_template`, which take the same fields
(`preview_template` has no `?priority=` or `?tags=` override).
