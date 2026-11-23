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

- Web: subscribe and unsubscribe to topics, per user (#617). A card's
  snooze menu has Unsubscribe from the topic, with Undo; Settings, Topics
  lists every topic with a switch and a switch for new topics. An
  unsubscribed topic leaves the inbox, counts and devices but still opens
  from its link, with a Subscribe button. Unsubscribing from agents warns
  first, since approval requests go there.

- Web: the foot of Settings links to the source code and to the issue tracker
  for reporting a problem, like the Android app (#586).
- Topic subscriptions on the server (#617): unsubscribe from a topic and
  it leaves your list, counts, live stream and Web Push on all your
  devices, without clearing anything or changing what anyone else sees.
  Everything stays subscribed until you choose otherwise. Set it through
  the API or an agent over MCP; the web and Android screens come next.
- Android: Settings is now in the same sections as the web (Notifications,
  Inbox, Devices, Integrations) with a search box at the top (#575). It
  matches a setting's name, its description or keywords such as "qr", and
  says so when nothing matches. Settings also links to the source code and
  to the issue tracker for reporting a problem (#586).
- Color rules on the server: color cards by topic, tag, priority or title text, as a bar, tint, solid fill or gradient, set through the settings API or an agent over MCP. Senders can give a message its own color. The web and Android screens for them come next.
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

### Changed

- Web: the Message format check in Settings is one card per topic with a
  count of its findings, a Dismiss menu on each finding and Copy fix prompt as
  the main button, instead of one block of text and buttons (#618). With more
  than three topics the cards start closed.

### Fixed

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
