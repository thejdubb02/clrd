# Jira

`POST /{topic}?template=jira` takes Jira Cloud issue webhooks. Each issue is
one notification, updated as it changes and resolved when its status reaches
the Done category or it is deleted.

1. Jira settings, System, WebHooks, Create a WebHook.
2. URL: `https://clrd.example.com/tickets?template=jira&auth=<token>`
   ([how to make `<token>`](custom-webhooks.md#sending-the-token)).
3. Events: issue created, updated and deleted. Add a JQL filter to narrow it.
4. If you set a secret (the REST API registration supports one), save it in
   clrd so it checks `X-Hub-Signature`:
   `curl -X PUT https://clrd.example.com/api/v1/topics/tickets/templates/jira -H "Authorization: Bearer clrd_..." -d '{"secret":"..."}'`

| clrd | Jira |
|---|---|
| `title` | `issue.key` and `issue.fields.summary`, e.g. `OPS-12: Backups failing` |
| `message` | the status name and the user who changed it |
| `click` | the issue page (`/browse/<key>` on your site) |
| `external_id` | `jira-<issue key>` |
| resolve | `issue.fields.status.statusCategory.key` is `done`, or `jira:issue_deleted` |

Source: [Jira Cloud webhooks](https://developer.atlassian.com/cloud/jira/platform/webhooks/).
