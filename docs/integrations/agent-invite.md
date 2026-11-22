# Agent invite

One short string gives an AI agent (or any bot) its own clrd user and token,
limited to the topics you name. You never copy a token by hand.

## 1. Make the invite

In the Android app: Settings, Linked devices, Add agent. Type its name and
topics, pick how long the invite lasts (15 minutes unless you change it),
then copy or share the https form below.

From the API, signed in as a person (a browser or phone session) or with a
full admin token:

```sh
curl -sS -X POST https://clrd.example.com/api/v1/agents/invite \
  -H "Authorization: Bearer $CLRD_TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"build-bot","topics":["build","deploys"],"expires_in":"15m"}'
```

Or from the server box, with an admin token:

```sh
CLRD_URL=https://clrd.example.com CLRD_TOKEN=clrd_... \
  clrd agent invite --name build-bot --topics build,deploys
```

Or from an agent that already has an admin token, with the MCP tool
`create_agent_invite(name, topics, expires_in?)`.

The answer holds two forms of the same invite:

- `clrd://agent?code=...&server=https://clrd.example.com`
- `https://clrd.example.com/agent?c=...`

Each invite works once and lasts 15 minutes unless you set `expires_in`
(1m to 1h). Hand it to the agent privately, like a password.

## 2. The agent claims it

An agent that can only fetch a URL opens the https form. It gets a plain text
guide: what it may do, where to publish, and the one command to claim. Opening
the page spends nothing and shows no token.

The claim is one call with no auth:

```sh
curl -sS -X POST https://clrd.example.com/api/v1/agents/claim \
  -H 'Content-Type: application/json' -d '{"code":"..."}'
```

It answers once with `token` (shown only here), `user`, `name` and `guide`:
the publish URL, the notification URL to read a reply from, the event stream,
the MCP address, its topics and the server's limits. Every later claim of the
same code gets 410.

## What the agent may do

- Read and publish on the invite's topics only. Each topic gets the `read`
  and `publish` the person who made the invite has there, never more. Only an
  admin may name a pattern such as `ops-*`.
- Ask for an answer (`actions` buttons, `wait_for_reply`), but never answer
  its own question: its approvals start `denied`, and it cannot raise that
  itself. It may ask a person with `POST /api/v1/devices/approve-request`.
- It cannot make invites, tokens or users.

## See it and remove it

The agent shows in your device list (kind `agent`, with last seen) and in the
admin user list. Unlinking it there revokes its token, ends its open streams
at once and deletes its user.

## Not supported

- Invites live in the server's memory: a restart drops any not yet claimed.
- Access is fixed when the agent claims. Changing the person later does not
  change the agent; an admin edits the agent's user instead.
- No "Add agent" button in the web inbox yet.
- No per-agent rate plans. An agent cannot invite another agent without a
  person or an admin token.
