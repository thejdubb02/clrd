# Proxmox VE

`?template=proxmox` takes a body you set on a Proxmox VE webhook notification
target (Proxmox VE 8.3+). Error severity is priority high, info is low.

1. Datacenter, Notifications, Add, Webhook.
2. Method `POST`, URL `https://clrd.example.com/pve?template=proxmox`.
3. Secrets: add `token` with your clrd token (and `clrd` with a shared secret
   if you want the [secret check](custom-webhooks.md#secrets)).
4. Headers: `Content-Type: application/json`,
   `Authorization: Bearer {{ secrets.token }}`, and optionally
   `X-Clrd-Secret: {{ secrets.clrd }}`.
5. Body:

   ```
   {"title": "{{ escape title }}", "message": "{{ escape message }}", "severity": "{{ severity }}"}
   ```

6. Add a matcher that routes the events you want to this target.

| clrd | body |
|---|---|
| `title` | `title` |
| `message` | `message` |
| `priority` | high for `error`, low for `info`, else 3 |

Source: [Proxmox VE notifications](https://pve.proxmox.com/wiki/Notifications).
