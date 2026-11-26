# Install the web inbox

Open `/app` on your clrd server over HTTPS and sign in.

- **Chrome and Edge:** choose Install in the address bar or browser menu.
- **iPhone and iPad (16.4 or later):** open the inbox in Safari, use Share,
  then Add to Home Screen. Open clrd from the new icon to turn on browser
  notifications. [WebKit's platform notes](https://webkit.org/blog/13878/)
- **Safari on macOS Sonoma or later:** choose File, then Add to Dock.
  [Apple's instructions](https://support.apple.com/104996)

In clrd, go to Settings, Browser notifications, and choose Turn on
notifications. Allow notifications when the browser asks. Installation and
notification permission are separate. The inbox still works in a browser tab.
Web Push uses HTTPS or localhost and the browser's own push service. clrd
creates and stores its VAPID keys automatically. You need no provider account
or Firebase configuration.

When you lose the connection, an installed worker shows "Lost the live
connection" on navigation. Reconnect and choose Reload. The worker keeps no
offline copy of private inbox content.

Clear an item in the inbox to sync the change. Closing a system notification
only hides it on that device. An open inbox removes browser alerts cleared or
resolved on another device when the change applies to this browser. If all
inbox windows are closed, their system alerts are checked when you open the
inbox again. clrd sends background pushes for new alerts, repeats and snoozes
ending, so a clear never triggers a new system notification.

## Set up a subscription through REST or MCP

Use the same read-scoped device token for each call. Get the server's VAPID
public key with `GET /api/v1/push` or `push()` on MCP. After the user grants
permission, pass that key as `applicationServerKey` to
`PushManager.subscribe` with `userVisibleOnly: true`. Send the resulting
subscription JSON (`endpoint` and `keys`) to `PUT /api/v1/push` or MCP
`set_push(endpoint, keys)`. Keep that JSON private.

`DELETE /api/v1/push` or MCP `delete_push()` stops delivery from the server.
To remove the browser's copy too, call `unsubscribe()` on the subscription or
turn notifications off in clrd Settings.
