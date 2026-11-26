# GitHub

`POST /{topic}?template=github` takes GitHub's repository or organization
webhooks. An issue or pull request is one notification, updated by each event
on it and resolved when it closes. A failed workflow run or check is priority
high.

1. Repository (or organization) Settings, Webhooks, Add webhook.
2. Payload URL: `https://clrd.example.com/dev?template=github&auth=<token>`
   (how to make `<token>`: [custom-webhooks.md](custom-webhooks.md#sending-the-token)).
3. Content type: `application/json`. The form type is not supported.
4. Secret: any long random string. Save the same one in clrd so it checks
   `X-Hub-Signature-256`:
   `curl -X PUT https://clrd.example.com/api/v1/topics/dev/templates/github -H "Authorization: Bearer clrd_..." -d '{"secret":"..."}'`
5. Pick the events you want. The ping GitHub sends on save shows up too.

| clrd | GitHub |
|---|---|
| `title` | `repository.full_name`, the `X-GitHub-Event` header and `action`, e.g. `octo/app: issues opened` |
| `message` | the issue, pull request, workflow run, check run or release title, or the head commit message |
| `click` | the matching `html_url` (comment first), else the compare or repository URL |
| `external_id` | `owner/repo#number` for issues and pull requests |
| resolve | `action: closed` on an issue or pull request |
| `priority` | high when `conclusion` is `failure`, else 3 |

Payloads: [GitHub webhook events](https://docs.github.com/en/webhooks/webhook-events-and-payloads).
Signature: [validating deliveries](https://docs.github.com/en/webhooks/using-webhooks/validating-webhook-deliveries).
