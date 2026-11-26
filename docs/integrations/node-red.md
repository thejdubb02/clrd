# Node-RED

The clrd action node can send, acknowledge, clear, resolve or snooze a
notification. The clrd events node starts a flow for new notifications
or state changes.

## Install from source

The package is unpublished. It targets Node-RED 4 or later on Node.js 22 or
later. Run this from a checkout of this repository:

```sh
cd sdk/node-red
npm pack
```

Install the resulting `node-red-contrib-clrd-0.1.0.tgz` from the Node-RED
user directory (usually `~/.node-red`) and restart Node-RED:

```sh
cd ~/.node-red
npm install /absolute/path/to/node-red-contrib-clrd-0.1.0.tgz
```

## Credentials and actions

In a **clrd** node, add a clrd server configuration with your server URL,
such as `https://clrd.example.com`, and a topic-scoped token. Grant
`publish` for Send and Resolve, and `read` for events and the other state changes. Node-RED stores
the token as a password credential outside the exported flow.

For Send, configure a Topic and pass a message string in `msg.payload`.
Alternatively pass an object:

```json
{
  "topic": "infra",
  "message": "Disk 91%",
  "title": "Build host",
  "priority": 4,
  "external_id": "build-disk"
}
```

For Acknowledge, Clear, Resolve or Snooze pass the server-assigned
notification ID as `msg.payload` or as `msg.payload.id`. For Snooze, set the
duration in the editor or override it with `msg.payload.duration`, such as
`"15m"`. The node replaces only `msg.payload` with the returned notification.
Use a Catch node to handle request errors.

## Events

Add a **clrd events** node. Choose New Notification or State Change, set
a polling interval in seconds, and optionally enter a topic. The flow gets
one message per matching event, with the event in `msg.payload` and its
topic in `msg.topic`. The event contains `type`, `seq` and
`notification`.

The event cursor lives in memory. Each deployment starts from the
server's current position. After a request failure the node waits longer
between polls, up to one minute or the configured interval if it is longer.
A successful poll restores the configured interval. If the server resets its event history or the
cursor falls outside retention, the node logs a warning and starts at the
current position again.
