# Changelog

Every change a user would notice goes here, newest first. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions will
follow [Semantic Versioning](https://semver.org/) once releases start.

Add your entry under `Unreleased`, in the section that fits (Added, Changed,
Fixed, Removed, Security), as one line a user can understand.

## Unreleased

### Added

- MCP: the `send` tool takes `format` (`markdown` or `text`), so an agent can
  send plain text that is shown exactly as written.
- Server: auto-clear. Publish with `"expires": "30m"` (or a time) and the
  notification resolves itself then, leaving every inbox and phone.
- Missed check-in alerts: give a topic an expected interval (for example
  `{"checkin":"25h"}` on `PUT /api/v1/topics/{topic}`) and clrd raises an
  urgent notification when no message arrives in time. The next message
  resolves it on every device.
- `CLRD_PUBLIC_PAGES=off` for a private server: the address opens the inbox
  instead of the landing page, the docs are gone, and search engines are told
  to stay away.
- ntfy compatibility: `GET /v1/account`, which the ntfy app and CLI use to check
  a login and scripts use to check a token still works.
- Web: a command palette on Ctrl+K (Cmd+K on a Mac) that lists every keyboard
  shortcut: J and K to move between notifications, C to clear, A to
  acknowledge, S to snooze, / to search, and G then I, S, C or comma to change
  page.
- Web: Snooze uses a default time from Settings. Long press it, right click
  it, or tap the arrow next to it for the other times. Settings has a Snooze
  section to pick up to 8 times and the default, shared by every device.
- Web: times read as "5 min. ago" and similar. Hover a time to see the exact
  date.

- Android: messages show their formatting (bold, italic, underline, strike,
  code, lists, quotes, headings and tappable links) in the list, and bold and
  italic in the system notification. A message sent as plain text is shown
  exactly as sent.
- Web: a component guide at `/app/ui` showing every button, chip, field, menu
  and dialog in light and dark. It is the start of one look for every screen.
- Android: Share and Copy on each notification. Icons show in the tray and the
  list, picture attachments show as a preview, and other files get a Download
  button.
- Server: webhooks in. `POST /{topic}?template=<name>` now has built-in
  templates for GitHub, Sentry, Stripe, Jira, Healthchecks.io, Proxmox VE,
  Zabbix, Netdata, Jellyfin and the *arr apps, checks GitHub, Sentry, Stripe
  and Jira signatures, and takes a template you save for any other service's
  JSON (`/api/v1/topics/{topic}/templates`). See
  [docs/integrations/custom-webhooks.md](docs/integrations/custom-webhooks.md).
- Server: message formatting. Each notification is Markdown or plain text
  (`format`, or ntfy's `X-Markdown: no`); the default is Markdown, so ntfy apps
  now render clrd messages as Markdown unless sent with `X-Markdown: no`.
  Display settings (formatting, links, site icons, link previews, per-topic
  previews) are stored on the server and sync to every device. New site icon
  and link preview endpoints fetch pages for your devices, refuse private
  network addresses, and cache results. The web inbox and Android app do not
  use them yet. See docs/formatting.md.
- Docs: every integration guide now runs against a live clrd in CI, with the
  real tool where one can run (Gatus, Uptime Kuma, Alertmanager, Grafana,
  Apprise, the ntfy CLI, Claude Code and Claude Desktop). The integrations
  table says what each one checks. The Gatus guide now sets an alert
  description, so the notification is not blank, and the Apprise guide no
  longer claims priority, tags and click URL work (#130).
- Server: `clrd import ntfy` moves an ntfy server into clrd: users, bcrypt
  passwords (ntfy routes only), `tk_` tokens, per-topic access rules (read-only,
  write-only, deny, wildcards), anonymous access, message history and
  attachments. `--dry-run` lists what maps and what does not. Tokens can now be
  limited to certain topics with read, publish or both. Ignored ntfy publish
  options are counted at `/api/v1/ntfy/ignored`. Guide: docs/migrate-from-ntfy.md.
- Server: files on notifications. Upload a file with the publish (multipart, or
  ntfy's `X-Filename` / `PUT` with the file as the body) or link one by URL
  (`attachment`, or ntfy's `X-Attach`). Downloads need a token that can read the
  topic. Files are capped at 15 MB each and 5 GB in total, and go away with
  their notification. The web inbox does not show them yet.
- Android: the app notices a server that was reinstalled or moved to a fresh
  database and reloads its list, instead of showing notifications from the old one.
- Server: an icon on each notification (`icon`, or ntfy's `X-Icon`), and a
  default icon per topic so each service gets its own picture. The web inbox
  does not show it yet.
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

### Changed

- Web: every page (inbox, snoozed, cleared, settings, sign-in, setup, the
  home page and the docs) uses the new design, with dark mode that follows
  the system. Clearing a notification plays one short "cleared" motion, which
  is off when the system asks for reduced motion.

- Web: the final logo. The three lines now end on one circle around the
  green disc, so every gap is the same. The README and site header use it.

### Fixed

- Web: the open Snooze menu stays on screen on a phone, and on a wide screen
  it no longer cuts off the custom time box.
- ntfy compatibility: a JSON publish to `/` now reads priority, title, tags,
  click URL and icon from headers when the JSON leaves them out, as ntfy does,
  so Apprise's `?priority=high` arrives.
- Android: the always-on "in sync" notice no longer puts a dot on the app icon.
- Web: the inbox no longer scrolls sideways on a phone (long titles, the
  closed Snooze menu and the action row pushed it wider than the screen), and
  code blocks and tables that scroll can be scrolled from the keyboard.
- Android: reopening a notification from the inbox, and the end of a long
  catch-up after being offline, no longer make the phone buzz; with sync off,
  a busy server's retry wait is respected; tray buttons on notifications from
  older app versions work again.

### Security

- Server: publishing is rate limited, 60 a minute per token and 60 a minute per
  topic by default, so a looping script cannot flood every device. Over the cap
  gets 429 with `Retry-After`. Set `CLRD_RATE_TOKEN` and `CLRD_RATE_TOPIC` to
  change them, or `0` to turn one off.
- ntfy compatibility: ten wrong passwords from one address now block password
  logins from it for a while (429 with `Retry-After`, one more try each
  minute). Tokens are never blocked. Behind a proxy, the client address comes
  from `X-Forwarded-For`; the nginx example in the self-host guide now sets it.
- Server: a token that can publish to a topic but not read it no longer gets
  another sender's message back when it acks, resolves or overwrites one by
  external id.
