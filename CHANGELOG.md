# Changelog

Every change a user would notice goes here, newest first. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions
follow [Semantic Versioning](https://semver.org/).

Add your entry under `Unreleased`, in the section that fits (Added, Changed,
Fixed, Removed, Security), as one line a user can understand.

The server binary carries this file and shows it as its version history.
`go run ./server/cmd/cutversion X.Y.Z` cuts a version (`-summary-file F`
keeps a short summary here and puts the full entries in the archive), and
moves the oldest
ones to `CHANGELOG-archive.md` in the repo when this file nears
96,000 bytes, so the archive holds the rest of the history.

## Unreleased

### Added

- Web Settings has a Color rules section: add, order and delete rules, with Severity and By topic presets and a note when a color looks like clrd's clear green; docs page docs/color-rules.md (#165).
- Slack outgoing hooks post notifications, edit them on state changes, and add acknowledgment and clear reactions (#166)
- Stats adds a server-time heatmap, prior-period trends, volume breakdowns, same-token clears, quiet alarms, and live connections on the Stats page and API (#120).
- Integrations: n8n and Node-RED nodes can send, clear, acknowledge, resolve and snooze clrd notifications, and start workflows on new notifications or state changes (#113). Available to build from source.
- Install the web inbox on a phone or computer, with an offline screen and opt-in browser notifications. Clear or resolve an alert on another device and the open inbox removes its browser notification. Browser subscriptions can also be managed through MCP (#164).
- SDKs: the source-only create-clrd installer connects local AI agents to clrd with individual invites, configures MCP and hooks, and can remove the connections (#154).
- ntfy publish: headers carrying RFC 2047 encoded words (for example `Title: =?UTF-8?B?...?=`, as ntfy clients send non-ASCII text) are decoded, the same as on ntfy (#842).

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

### Changed

- Server: the link in a GitHub issue opened by a hook now goes to `/app/n/<id>`, which opens that notification in the inbox, snoozed or cleared view, wherever it is now, instead of an inbox link that found nothing once it was cleared or snoozed (#814).
- Desktop: Unlink is one call to the server, `DELETE /api/v1/devices/this`, so it works whatever the computer's token may do (#643).
- Android: the phone unlinks itself with `DELETE /api/v1/devices/this`. A phone linked with a publish-only token, which cannot see Linked devices, now gets an "Unlink this phone" button in Settings, Devices (#643).
- Android: a stack's Clear all shows only in the inbox, as on the web. The Snoozed tab no longer offers to clear a whole stack of snoozed notifications at once (#108).
- Server: the release binary stores its docs, web files and API description gzipped, so it is about 500 KB smaller, and browsers get the web app's scripts, its stylesheet and the API description compressed (#690). Building from source: run `go run ./server/cmd/pack`, then build with `-tags packed`.
- Server and Android: five of the six built-in sounds are stored at the lowest sample rate that still holds every tone they play, so they sound the same and take 51 KB less in the server and the app (#690).
- Android: unsubscribing asks first for any topic the server marks as
  carrying approval requests, from the Topics page or a card's menu, not
  only for "agents" (#623). With an older server, "agents" still asks.
- Web: Unsubscribe warns on any topic that has had an approval request or
  question, not only `agents` (#623).

- Web: the Message format check in Settings is one card per topic with a
  count of its findings, a Dismiss menu on each finding and Copy fix prompt as
  the main button, instead of one block of text and buttons (#618). With more
  than three topics the cards start closed.

### Fixed

- create-clrd cancels cleanly, checks invite permissions before adding hooks, and explains when a rerun keeps existing settings (#864).

- Automation nodes report invalid credentials and inputs more clearly, and the Node-RED trigger slows its retries when polling fails (#862).
- Event polling with `subscribed=all` keeps its cursor through subscription edits, so automation triggers can deliver notifications queued between polls (#868).

- Server: a GitHub issue hook still finds the issue it already opened after the issue was edited on GitHub's website, so a retried delivery does not open a second one (#827).
- Server and web: a token from the server's config file can no longer be revoked from the sender page or the API (409), because the next restart made it again from the file and the revoke silently undid itself. Remove it from the file instead; the page now says so and hides Revoke (#832).
- Web: the sender page no longer offers Rename or Rotate secret for a token set in the config file, which the server always refuses; it says to change it there. Senders carry `config: true` for such tokens (#119).
- Web: a notification's own page shows "This notification is gone" once it is deleted or its topic is no longer yours, instead of keeping the card and retrying every few seconds (#826).
- Web: a notification's link (`/app/n/{id}`, as a GitHub issue links it) opens a page with that card alone, in any state, with a link back to its list, so it is there even past a list's newest 500 or when this device cleared it for itself alone (#822).
- Android: dark mode uses the same quiet grey and urgent orange as the web inbox (`#98A1AC` and `#F97316`), so the two match side by side (#583).
- Server: adding or removing `?wait=true` on a Discord webhook's URL no longer counts as a new channel, so the next wake edits the message instead of posting it again (#816).
- Android: a display or swipe setting changed on the web while the phone was saving its own change now keeps the web's newer value, a link preview never follows a redirect away from your server, and an open message shows its preview right after previews are turned on (#818).
- Server: a Discord webhook moved to a new URL now posts a scheduled or snoozed notification there when it wakes, instead of skipping it because the old URL had it, even when the move is saved while a post is going out. A wake also tries again a post that failed earlier (#795).
- Server: pausing GitHub issue hooks (`github_hooks_paused`) now also stops a send already under way, and issues waiting out a pause longer than `CLRD_RETENTION` are no longer dropped, so they all go out on resume (#796).
- Android: turning Markdown formatting on or off now redraws the notifications already in the shade at once, without a sound; and a link written as `<https://...>` shows without its brackets and stays a link even with automatic links off (#809).
- Server: deleting an outgoing webhook no longer erases its past delivery failures from Stats, and the Stats CSV export quotes a value that starts with `'`, so `-disk` and `'-disk` no longer read back the same (#794).
- Server: the Stats CSV export writes a carriage return in a text cell as `\r` and a backslash as `\\`, so an external id with a CR LF line break no longer reads back the same as one with a bare LF (#804).
- Server: with "Clears everywhere" off, MCP `wait_for_reply` now returns `dismissed` when the caller clears the notification for itself during the wait, as it already did after; a web push no longer reaches a device that hid the card while the push waited to send; and a big clear on one such device no longer makes your other devices' live streams drop and reconnect (#777).
- Server and web: "Unlink devices unused for 7 days" unlinks only the devices it counted, and it and the idle sweep check each device again as they unlink it, so one used, given web push or signed in under in the meantime stays. A browser signing in again no longer loses a lower approve setting saved at the same moment. `POST /api/v1/devices/tidy` takes an optional `devices` list (#773).
- Android: the formatting and web address link switches in Settings now apply on the phone too, in the inbox and the notifications, instead of only on the web inbox (#107).
- Android: in Settings, the Clears everywhere switch and the device menus stay off until a change has reached the server, so a quick second tap no longer sends it twice (#777).
- Android: when a cleared card leaves the list, the space under it now closes with it in one smooth move, as on the web, instead of snapping shut at the end (#726).
- Desktop (Linux): with GTK set to prefer dark and no desktop preference, picking Light and then System in Appearance turns the window dark again at once, instead of only after a restart (#783).
- Desktop (Linux): a clear that arrives just after the notification service restarted no longer closes another app's notification that took the old one's number (#601).
- Desktop (Linux): after the notification service restarts, an update to a clrd notification no longer replaces another app's notification that got the same number, and a button pressed on a notification from before the restart no longer acts on a different one (#791).
- Desktop (Linux): if the keyring quits while its unlock dialog is open, clrd stops waiting and says so instead of hanging. Keyrings that lock each entry on its own are now unlocked entry by entry, and only the keyring itself can end the wait for its dialog (#608).
- Android: a phone set up by pasting a token now follows the topics you unsubscribed from on the web. Before, it kept showing cards of those topics, so a clear on the web looked like it never reached the phone. After the update the phone drops them on its own; nothing to set up. Scripts using the same token still see every topic (#764).
- Web: Undo after an Unsubscribe sends `if_request_id`, so if another device changed topic subscriptions before the click, the server refuses it and the page shows "Changed on another device." instead of reversing their choice (#716).
- GitHub issue hooks: one alert never opens two issues. A retry after GitHub's answer was lost, or after a long alert whose answer was too big to read, now finds the issue already opened. The limit of 10 issues an hour also holds after an outage clears (#680).
- Web: a new card on a busy or slow device still slides in and wiggles, instead of skipping it when the page could not draw in time (#714).
- Desktop: the tray and app icon has a thin light ring, so it stays easy to see on a dark panel or taskbar as well as a light one (#628).
- Desktop (Linux): after the window has been made light for a desktop that asks for light, a GTK theme picked on the desktop later now shows at once instead of at the next start, on desktops that hand GTK their theme while running (#756).
- Desktop (Linux): a desktop that asks for light now gets a light window even when the GTK theme is a dark one by name, such as Adwaita-dark. The dark theme comes back when the desktop asks for dark or states no preference. A theme forced with the `GTK_THEME` variable still wins (#703).
- Android: the test that checks hostile Markdown renders fast no longer fails on a busy build machine, so a release build is not held up by it (#663).
- Android: a card that was snoozed and woke up, or was cleared and reopened, while the phone was offline now alerts when the phone catches up, as it did on the devices that were online. Your own Undo stays quiet (#645).
- Android: Clear on a card no longer wraps onto three lines on a common phone. Share and Copy now sit in the card's More menu, and on a narrow screen or with large text Ack and then Snooze move there too, so Clear always shows whole (#734).
- Desktop (Linux, build from source): flipping "Closing clears everywhere" no longer changes what happens to a notification you closed just before, while the app was still busy with an earlier clear. Each close follows the setting as it was when you closed it (#685).
- Desktop (build from source): a notification that was snoozed and woke up, or was dismissed and reopened, while the computer was offline now alerts when the app catches up, as it did on the devices that were online (#646).
- Android: Settings shows Snooze times and Linked devices only once the
  server accepts the token for reading, and hides Senders as soon as the
  token is rejected, instead of after the next refresh (#604).
- Android: a notification cleared on another device no longer comes back on
  the phone when a refused tap's late answer arrives after the phone caught
  up (#34).
- Android: the message that says how an approval tap ended (sent to confirm,
  denied, changed) shows once, even when the app restarted before the tap was
  sent again (#383).
- Android: a late repeat of a notification you just cleared, acknowledged or
  snoozed on the phone stays quiet even when the app restarted in between, and
  the phone forgets these marks once the notification is gone (#561).
- Android: in a right-to-left language, Settings names the swipe sides the
  way the gestures go there. A settings message from the old server that
  arrives just as you switch servers no longer brings back its swipe or
  snooze choices, and a failed swipe save no longer shows an older choice
  when another device changed it meanwhile (#568).
- Web: a subscriptions change from another device no longer drops a half-typed answer, and Undo shows only for an Unsubscribe that went through (#649).
- Web: a half-typed answer on a card whose topic was unsubscribed on another device comes back when the topic is subscribed again in the same tab (#687).
- Web: Unsubscribe from a card warns that approval requests will stop, even after the question that flagged its topic has left the inbox (#700).
- Web: Undo after an Unsubscribe shows only when this tab made the last change, so it never reverses another device's unsubscribe (#702).
- Web: a card reopened for later on another device that wakes while a Clear all is still waiting for its reply now comes back with its slide in and wiggle (#656).
- Docs: the self-host build command strips debug data, so the binary it
  makes is about 7.5 MB smaller, the same size as the release builds (#683).
- Web: a Settings search typed while the page is still loading now finds
  Link a phone once it appears (#591).
- Server: `color_rules: null` in a settings change is now refused with a
  400 like the swipe keys, instead of quietly changing nothing, and a rule
  field of the wrong type names the rule it is in (#615).
- Web: a snooze that ends while its card is still leaving now brings the card back with its slide in and wiggle.
- Web and Android: the sender's Alarm topic hint now says an empty topic
  means the first topic its last topic routes to, or that topic when it has
  no route, which is what the server does (#585).
- Server: a linked device can always unlink itself, even when it was linked
  from a publish-only token, with `DELETE /api/v1/devices/this` (#609).

## 0.1.0 - 2026-11-23

### Added

- First version of clrd: a self-hosted notification server in one Go binary with SQLite, a web inbox and an Android app. Clear, snooze or resolve a notification on one device and it clears on every device, and a device that was offline catches up when it reconnects.
- Works with no settings: start the server, open the setup link it prints, and send your first notification. Docker image build and a Compose file are included.
- ntfy compatible: ntfy publish and subscribe URLs, headers and clients work, and `clrd import ntfy` moves an ntfy server in with its users and messages.
- Gotify and Pushover senders work too, and `clrd import gotify` copies a Gotify server in.
- Web inbox with Inbox, Snoozed and Cleared views, stacks by topic, keyboard shortcuts with a command palette, a Send box, and live sync status for every device.
- Android app on clrd's own F-Droid repo, with no Google services: live sync, tray notifications that leave the phone when cleared elsewhere, and set up by scanning a code from the web inbox.
- Sign a browser in with your phone, or link a phone from the web inbox, with a QR code.
- Approve agent actions from your phone: a Claude Code plugin and a Codex hook send each permission request to clrd with Allow and Deny buttons, and each device has an approve setting (Allowed, Ask or Denied).
- Questions with reply buttons, including typed answers, so a script or agent can wait for your answer.
- MCP endpoint so AI agents can send, read, clear and resolve notifications and set up every feature, plus agent invites that give an agent its own token.
- Rich messages: Markdown formatting, emoji shortcodes, icons, link previews with site icons, file attachments, and ntfy-style action buttons.
- Alerts that repeat until someone acts, auto-clear after a set time, and resolve by the sender's own ID.
- Silences for a topic or tag, and alarms when a topic misses its check-in or a sender goes quiet.
- Senders page: see every service that sends to you, and pause, mute or reroute one with its own rules.
- Send to chosen devices only, and mark a notification seen or actioned with a note and a link.
- Notification sounds per device, set on the phone, in the browser or by an agent.
- System notifications in the browser through Web Push.
- Settings in five sections with search, covering topics, how long to keep cleared messages, snooze defaults, swipe buttons, linked devices and integrations.
- Webhooks in, with built-in templates for common services, and outgoing webhooks with their own body templates and a delivery log.
- Users with many devices, tokens with read, publish and admin scopes, and a config file for users and tokens.
- SDKs for Python, JavaScript and Go, a GitHub Action, and integration guides that are each tested against a live server.
- Version history: `GET /api/v1/version`, the changelog page in the docs, and the MCP `version` tool.

### Security

- Publishing is rate limited, repeated wrong ntfy passwords block the address, and link previews and site icons are fetched only from public addresses.
