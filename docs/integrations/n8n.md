# n8n

The clrd action node can send, acknowledge, clear, resolve or snooze a
notification. The clrd Trigger node starts a workflow for new notifications
or state changes.

## Install from source

The community package is unpublished. On a machine with Node.js 22 or later,
run these commands in a checkout of this repository:

```sh
cd sdk/n8n
npm ci --ignore-scripts
npm pack
```

Install the resulting `n8n-nodes-clrd-0.1.0.tgz` in your self-hosted n8n
installation. For a standard installation, put the archive next to the n8n
data directory, then use:

```sh
mkdir -p ~/.n8n/nodes
cd ~/.n8n/nodes
npm install /absolute/path/to/n8n-nodes-clrd-0.1.0.tgz
```

Run this as the user running n8n and restart n8n. In Docker, install inside
the container against the mounted n8n data directory. This package targets
n8n 2.x. See [n8n's manual installation guide](https://docs.n8n.io/integrations/community-nodes/installation/manual-install/).

## Credentials and actions

Create clrd API credentials inside n8n using your server URL, such as
`https://clrd.example.com`, and a topic-scoped clrd token. Grant `publish`
for Send and Resolve, and `read` for the trigger and the other state changes. Use both if the
workflow sends a notification and later changes it.

Add a **clrd** node and choose an operation. Send takes a topic, message,
and optional title, priority or external ID. For the other operations pass
the server-assigned Notification ID, for example `{{ $json.id }}` from
Send or `{{ $json.notification.id }}` from a trigger. For Snooze also
give a duration such as `15m`. Each action returns the notification.

## Trigger

Add **clrd Trigger**, choose New Notification or State Change, and set
n8n's poll interval. Leave Topic blank to watch all topics the token can read.
A live workflow starts at the current event position on its first poll.
It outputs one item per matching event with `type`, `seq` and
`notification`. Updates and repeats are separate event types. Testing the
trigger in the editor returns the latest readable notification as a sample,
if there is one, without moving the live cursor.

n8n saves the cursor with workflow static data after a successful execution.
Use the event sequence to deduplicate deliveries within the same server
history if your workflow retries work. If the server's event history is reset
or the cursor falls outside retention, the trigger logs a warning and
resumes at the current head.
