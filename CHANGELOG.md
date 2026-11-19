# Changelog

Every change a user would notice goes here, newest first. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions will
follow [Semantic Versioning](https://semver.org/) once releases start.

Add your entry under `Unreleased`, in the section that fits (Added, Changed,
Fixed, Removed, Security), as one line a user can understand.

## Unreleased

### Added

- Android: the app notices a server that was reinstalled or moved to a fresh
  database and reloads its list, instead of showing notifications from the old one.
- Server: one Go binary with SQLite. Native publish API, notification states
  (dismiss, acknowledge, resolve, snooze) synced to every device over WebSocket
  and SSE, with catch-up from the last seen event after a reconnect.
- Server: ntfy-compatible publish and subscribe (JSON, SSE, WebSocket, filters,
  Basic auth), so most ntfy scripts and the ntfy app work unchanged.
- Server: resolve an alert by the sender's own ID, plus templates for
  Alertmanager and Grafana webhooks.
- Server: tokens with read, publish and admin scopes and optional topic lists;
  one-time setup link on first start; `/healthz` reports failing background jobs.
- Server: MCP endpoint so AI agents can send notifications, resolve them and
  wait for a reply.
- Web: landing page, docs served from `docs/`, and the inbox with Snoozed,
  Cleared and Settings. Live sync, Undo, dark mode, phone width, and the main
  actions work without JavaScript.
- Web: sign in with a token and name the browser; reply buttons on cards; a
  notice when live sync has been down for 3 minutes.
- Android: app with live sync, tray notifications that clear when cleared
  anywhere else, link and reply buttons in the tray, F-Droid metadata. No Google
  Play Services.
- Android: tap a notification in the inbox to read the whole message; the open
  text can be selected and copied.
- Android: pasting a link into the Server URL field replaces the prefilled
  `https://`, and a pasted setup or page link is cut down to the server address.
- Android: signed builds are published to the project's own F-Droid repo on
  every change, so the F-Droid app can install and update clrd. The address is
  in `android/README.md`.
- SDKs for Python, JavaScript and Go, and a GitHub Action.
- Integration guides for curl, GitHub Actions, MCP clients, Home Assistant,
  Gatus, Uptime Kuma, Alertmanager, Grafana, Apprise and ntfy clients.
- Docker image build and a Compose file for self-hosting.
- Server: `ready`, the events poll and the notification list carry an `epoch`
  naming the database, so a client can tell when the server was reinstalled or
  started on a new database and fetch fresh. The Android app does not act on it
  yet, and a restored backup keeps its old epoch.
- Web: an open inbox reloads when the server comes back on a different database
  (a new epoch), instead of carrying on from a position that no longer exists.

### Fixed

- Android: reopening a notification from the inbox, and the end of a long
  catch-up after being offline, no longer make the phone buzz; with sync off,
  a busy server's retry wait is respected; tray buttons on notifications from
  older app versions work again.
