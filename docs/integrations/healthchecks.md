# Healthchecks.io

`?template=healthchecks` takes a body you set in a Healthchecks.io webhook
integration. A check going down opens a priority high notification, and the
check coming back up resolves it.

1. Integrations, Webhook, Add.
2. For both "Execute when a check goes down" and "goes up": method `POST`,
   URL `https://clrd.itsjustin.me/cron?template=healthchecks&auth=<token>`
   ([how to make `<token>`](custom-webhooks.md#sending-the-token)),
   header `Content-Type: application/json`, and this body:

   ```json
   {"name": "$NAME", "status": "$STATUS"}
   ```

3. Optional: add `&secret=...` to both URLs and save the same secret in clrd
   ([secrets](custom-webhooks.md#secrets)).

A check name containing a double quote breaks the JSON; rename it.

| clrd | body |
|---|---|
| `title` | `<name> is <status>` |
| `external_id` | `healthchecks-<name>` |
| resolve | `status: up` |
| `priority` | high when `down` |

Source: [How Healthchecks.io sends webhook notifications](https://blog.healthchecks.io/2024/10/how-healthchecks-io-sends-webhook-notifications/).
