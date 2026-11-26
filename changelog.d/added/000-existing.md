- ntfy compatibility: opt in to treating unnamed large or binary publishes as file uploads with the existing attachment limits (#851).

- Web Settings has a Color rules section: add, order and delete rules, with Severity and By topic presets and a note when a color looks like clrd's clear green; docs page docs/color-rules.md. Inbox cards now show their rule colors on a running server, which did not serve the card colors stylesheet (#165).
- Slack outgoing hooks post notifications, edit them on state changes, and add acknowledgment and clear reactions (#166)
- Stats adds a server-time heatmap, prior-period trends, volume breakdowns, same-token clears, quiet alarms, and live connections on the Stats page and API (#120).
- Integrations: n8n and Node-RED nodes can send, clear, acknowledge, resolve and snooze clrd notifications, and start workflows on new notifications or state changes (#113). Available to build from source.
- Install the web inbox on a phone or computer, with an offline screen and opt-in browser notifications. Clear or resolve an alert on another device and the open inbox removes its browser notification. Browser subscriptions can also be managed through MCP (#164).
- SDKs: the source-only create-clrd installer connects local AI agents to clrd with individual invites, configures MCP and hooks, and can remove the connections (#154).
- ntfy publish: headers carrying RFC 2047 encoded words (for example `Title: =?UTF-8?B?...?=`, as ntfy clients send non-ASCII text) are decoded, the same as on ntfy (#842).

- Android: add more than one server and switch between them; each keeps its own settings, connection and notifications (#78).
- Android: each server can have its own client certificate for mutual TLS, picked in Settings while that server is shown (#78).
- Desktop: the tray's About menu always has What's new (the server's history, or the project's when this computer is not linked), says plainly when this app is older or newer than the server, and reads "What's new in" the version for the first start after an update until it is opened (#580).
- Android: JVM screenshot tests render the inbox, an open card and Settings in light and dark mode to `android/screenshots/` for the README and CI verification (#658).
- Web: choose Default, High contrast, Midnight or a custom color theme with a live preview and contrast checks. The server saves it for your signed-in browsers, with optional device overrides and API and MCP support (#584).

- Web: Add a service on the Senders page. Pick a service (GitHub, Grafana, Sentry and the other webhook templates, or curl, ntfy, Gotify, Pushover), name it and pick a topic; clrd makes a token that can only publish to that topic and shows it once with a ready-to-paste URL or command and the service's guide, then says connected when its first message arrives (#119).
- Server and web: a sender can have its own icon, set in the rules form on its page, over the API (`PUT /api/v1/senders/{id}` `{"icon":"https://..."}`) or from an agent (`set_sender`). Its notifications that bring no icon get it, before the topic's icon; it shows on the Senders list and page, and the change can be undone like the other rules (#119).
- Rename, rotate and revoke a sender's token from its page in the web inbox, over the API (`PUT /api/v1/tokens/{id}`, `POST /api/v1/tokens/{id}/rotate`) or from an agent (`rename_token`, `rotate_token`). A rotated token keeps its id, rules and history; the old secret and the browsers signed in with it stop at once (#119).
- Android: Settings, Display, Formatting and links has the four switches the web inbox has (Markdown, web addresses as links, site icons, link previews). With link previews on, an opened message shows a card with the page title, summary and image, fetched by your server, and each topic's preview choice from the web is followed (#107).
- Android: a "Swipe clears everywhere" setting, off by default. With it on, swiping one notification out of the shade clears it on every device; swiping a group away or Clear all on several still only hides them on the phone (#271).
- Android: Settings, Devices has a "Clears everywhere" switch for this phone. Turn it off and clearing a notification on the phone hides it there only. Only an allowed device can change it; a phone that is not allowed is told so beside the switch (#767).
- Android: Settings, Devices, Client certificate picks a certificate installed on the phone for a server behind mutual TLS. The key stays in Android's key store and is offered only to the clrd server address it was picked for, never to a site a message links to (#152).
- Server and web: each device has "Clears everywhere", on by default. Turn it off and clearing a notification on that device hides it there only, while your other devices keep it; Undo brings it back. Set it in Settings, Devices, or with `PUT /api/v1/devices/{id}/clears` (#767).
- Server and web: a browser that signs in again replaces its own row in Linked devices instead of adding a new one, keeping any lower approve setting the old row had. Each browser and phone row also shows what it is (for example "Firefox 120 on Linux"), a Rename button names it, and "Unlink devices unused for 7 days" tidies old rows in one click. Linked browsers unused for 30 days now unlink on their own (setting `unlink_idle_browsers_days`, 0 turns it off); on upgrade, browsers already unused that long unlink within the hour. The phone and desktop apps do not send their install id yet, so they still add a row when they link again (#765).
- Android: linking the same phone again replaces its row in Linked devices instead of adding a second one, and the row shows the model and Android version (for example "Pixel 8, Android 15") (#769).
- GitHub issue hooks: issue titles now carry a `[clrd <topic>]` prefix, priority maps to `clrd` and `priority:*` labels, the issue body links back to the notification when `CLRD_BASE_URL` is set, and full admins can pause all GitHub issue hooks server-wide with `github_hooks_paused` in `PUT /api/v1/settings` (#680).
- Discord webhooks: Settings > Webhooks now has a Kind selector (plain webhook, Discord or GitHub), scheduled notifications post to Discord when they wake, and updating a notification in place re-edits its Discord message (#673).
- Stats: `GET /api/v1/stats` and the web Stats page now show failed outgoing webhook and Web Push deliveries per topic, export the same numbers as CSV (`?format=csv`), and `CLRD_METRICS=on` exposes optional Prometheus metrics at `GET /metrics` for full admins (off by default) (#661).
- Desktop (Linux): the title bar and the This computer page follow the Appearance pick in the inbox (Dark, Light or System), not only the desktop's own setting. The pick is kept for the next start (#760).
- Web: the first open after the server moves to a new version shows a small, dismissible What's new note linking to Settings, About, with a per-browser switch in Settings to turn it off (#580).
- Web, SDKs and F-Droid: the sign-in page now carries the shared footer so the GitHub link is visible on phones, the Go and Python SDK READMEs link to the repo, and the F-Droid metadata sets the changelog URL (#586).
- Desktop: linking the same computer again replaces its row in Linked devices instead of adding a second one, and the row shows its system (for example "Fedora Linux 41" or "Windows 11") (#770).
- Server and web: a browser that signs in again replaces its own row in Linked devices instead of adding a new one, keeping any lower approve setting the old row had. Each browser and phone row also shows what it is (for example "Firefox 120 on Linux"), a Rename button names it, and "Unlink devices unused for 7 days" tidies old rows in one click. Linked browsers unused for 30 days now unlink on their own (setting `unlink_idle_browsers_days`, 0 turns it off); on upgrade, browsers already unused that long unlink within the hour (#765).
- SDKs: the JavaScript client and GitHub Action READMEs link to the source repository on GitHub (#586).
- Android: a link in a message shows only its words, with the site's icon beside it (fetched through your server, as on the web) or a plain link icon. A tap opens it; a long press shows the full address with Open and Copy. The site icons follow the `favicons` setting (#107).
- Desktop (Windows): an administrator can set "Update automatically" for every user of a computer with the registry number `AutoUpdate` under `HKLM\Software\Policies\clrd` (0 off, 1 on). The switch then shows that answer and is greyed out. Only in builds made with an update address (#725).
- Android: cards move as on the web. A card cleared, snoozed or resolved while you look (here or on another device) shows a green wash and why, holds, then slides away and the list closes the gap; Clear all goes top to bottom. A new message drops in and the newest gives a small wiggle. Nothing moves on first load, on catch-up, or with Remove animations on (#598).
- Server: a subscriptions change can carry `if_request_id`, so it applies only while the set's last change is still that request; otherwise it is refused with 409 and nothing changes, so an Undo never reverses another device's change (#716).
- Server: a subscriptions change can carry a `request_id`, and the subscriptions list says which request made the last change, so the web can offer Undo only for its own Unsubscribe (#702).
- Server: the answer to ack, snooze, dismiss, resolve or reopen says `"changed": false` when it changed nothing (the notification already had that state), so an app retrying an action does not mistake it for its own change (#693).
- Web: a Stats page for admins, with notifications per hour or day, priorities, top topics and senders, how fast each gets cleared, and repeat alerts, over 24 hours, 7, 30 or 90 days (#120).
- Web: Settings, Appearance picks System, Light or Dark for this browser. It applies at once on every page with no flash, and System still follows the device (#583).
- Android: Settings, Display, Appearance picks System, Light or Dark for this phone. It applies at once, status bar icons included, and System still follows the phone (#583).
- Web: Settings has an About section with the version this server runs and
  a What's new list of what changed in each version, read from the
  changelog built into the binary (#580).
- Android: a Topics page in Settings lists every topic with its open count
  and last message, and turns each one on or off for your account on every
  device, plus whether new topics start subscribed (#617). A card or a
  stack's menu unsubscribes from its topic with Undo, and the "agents" topic
  asks first since it carries approval prompts. Muted topics stay a separate,
  this phone only setting.
- Server: traffic stats for admins at `GET /api/v1/stats` and the MCP
  `stats` tool: volume per hour, top topics and senders, how long alerts
  stay open before someone clears them (median and p90), who clears them,
  and alerts that keep firing (#120).
- Site: the landing page shows a sample web inbox, drawn by the real card
  template with fixed demo data, so it always matches the inbox and needs
  no pictures or JavaScript (#592).
- Web: subscribe and unsubscribe to topics, per user (#617). A card's
  snooze menu has Unsubscribe from the topic, with Undo; Settings, Topics
  lists every topic with a switch and a switch for new topics. An
  unsubscribed topic leaves the inbox, counts and devices but still opens
  from its link, with a Subscribe button. Unsubscribing from agents warns
  first, since approval requests go there.

- Server: the topics and subscriptions lists mark each topic that has
  carried an approval request or another message waiting for an answer
  (`approvals`), so apps can warn before you unsubscribe from it (#623).
- Web: the foot of Settings links to the source code and to the issue tracker
  for reporting a problem, like the Android app (#586).
- Topic subscriptions on the server (#617): unsubscribe from a topic and
  it leaves your list, counts, live stream and Web Push on all your
  devices, without clearing anything or changing what anyone else sees.
  Everything stays subscribed until you choose otherwise. Set it through
  the API or an agent over MCP; the web and Android screens come next.
- Android: Read on an unsubscribed topic in the Topics page shows its open
  cards without subscribing (#624). Nothing is saved on the phone and nothing
  rings; Subscribe there turns the topic back on.
- Android: Settings is now in the same sections as the web (Notifications,
  Inbox, Devices, Integrations) with a search box at the top (#575). It
  matches a setting's name, its description or keywords such as "qr", and
  says so when nothing matches. Settings also links to the source code and
  to the issue tracker for reporting a problem (#586).
- Color rules on the server: color cards by topic, tag, priority or title text, as a bar, tint, solid fill or gradient, set through the settings API or an agent over MCP. Senders can give a message its own color. The web inbox draws them on its cards, picks dark or white text on a solid fill by contrast, and recolors live when the rules change without closing an open menu; the Android screen comes next.
- Web: a cleared card now slides away and the cards below close the gap, on
  every kind of clear, and Clear all goes top to bottom (#598). A card that
  arrives while you are looking slides in with one small wiggle; nothing
  plays on load, after a reconnect or for cards that came while the tab was
  in the background, and nothing moves with reduce motion on. The timings are
  in SPEC section 10.1 so every app matches.
- Desktop app for Linux, to build from source for now (there is no download yet): a tray icon and a window on your inbox. Link it with a link from Settings > Devices; its token stays in the system keychain and never reaches the page in the window (#162).
- Desktop (Linux): a script that packs your own build into a `.deb` for
  Debian and Ubuntu (#625). The package brings the tray library with it,
  adds clrd to the applications menu and starts it with your desktop
  session. There is still no download. Without the tray library the app no
  longer stops at start: it opens its window instead.
- Desktop (Linux, with the package installed): "Open in the clrd app" on the
  link page opens the app with the link filled in (#626). The app names the
  server and waits for you to press Link this computer; it never links from
  an address by itself, since any web page can send one.
- Desktop (Linux, build from source): the This computer page has a
  "Closing clears everywhere" switch (#627). It is off at first, so closing
  a notification on the computer only hides it there; turn it on and a close
  clears it on all your devices. The choice is kept across restarts.
- Desktop (Linux, build from source): the window follows the desktop's light
  or dark setting, at start and when you change it (#628). Before, it only
  followed the GTK theme, so a dark desktop could show a light inbox.
- Desktop (Windows, build from source): the installer registers `clrd://` addresses, so "Open in the clrd app" on the server's link page opens the installed app with the link filled in (#162). Nothing is linked until you press Link this computer. Not yet tried on a Windows computer.
- Desktop (Windows, build from source): the installed app starts when you sign in to Windows, as a tray icon with no window (#162). Turn it off in Windows Settings > Apps > Startup; removing clrd removes the entry. Run under Wine only, not on a Windows computer yet.
- Desktop (Windows, build from source): the installed app shows each open notification as a Windows toast, with a Clear button that clears it everywhere (#162). Closing one only hides it on that computer unless "Closing clears everywhere" is on, and one cleared on another device leaves the screen and the notification centre. Built for Windows only: not seen on a Windows computer yet.
- Desktop (Windows, build from source): only one clrd runs per sign-in (#627). Starting it again opens the window of the one already running instead of a second app with a second tray icon. Run under Wine only, not on a Windows computer yet.
- Desktop (Windows, build from source): a build given an update address and a public key updates itself (#719). It runs only an installer signed with that key for the very version on offer; anything else is dropped and the running version carries on. "Update automatically" on the This computer page is on at first and can be turned off; Check for updates works either way. A build with no address never asks anywhere.
- Desktop (Windows, build from source): the app can be built into an installer that needs no administrator rights, adds clrd to the Start menu and can be removed from Windows Settings (#719). It is built on Linux and has not been run on a Windows computer yet. There is still no download.

- Server: outgoing webhooks can post to Discord (#166). A hook with
  `"kind": "discord"` posts each new notification to a Discord webhook URL and
  edits that message when it is cleared, acknowledged, resolved, snoozed or
  answered, so the channel shows what happened without anyone clearing it
  twice. Set it up through the API or an agent over MCP. Delivery errors no
  longer show a webhook's URL.

- Server: outgoing webhooks can open GitHub issues (#168). A hook with
  `"kind": "github"` opens an issue in a repo for each new notification,
  comments when it is acknowledged, answered or snoozed, and closes it when
  it is cleared or resolved, so a coding agent that works from issues can
  pick up an alert. Never for anonymous senders or questions, and at most 10
  issues an hour per hook. Set it up through the API or an agent over MCP.
