# Changelog

Released changes go here, newest version first. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions
follow [Semantic Versioning](https://semver.org/).

Pending changes live in [changelog.d](changelog.d/README.md). The server
shows them under Unreleased, and the release command collects them here.

The server binary carries this file and shows it as its version history.
`go run ./server/cmd/cutversion X.Y.Z` cuts a version (`-summary-file F`
keeps a short summary here and puts the full entries in the archive), and
moves the oldest
ones to `CHANGELOG-archive.md` in the repo when this file nears
96,000 bytes, so the archive holds the rest of the history.

## Unreleased

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
