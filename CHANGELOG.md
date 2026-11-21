# Changelog

Every change a user would notice goes here, newest first. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions will
follow [Semantic Versioning](https://semver.org/) once releases start.

Add your entry under `Unreleased`, in the section that fits (Added, Changed,
Fixed, Removed, Security), as one line a user can understand.

## Unreleased

### Security

- Server: a third approve setting, ask. A tap from an ask device does not
  count until an allowed device confirms it: clrd posts a "Confirm" prompt on
  the same topic, and the answer lands only when another allowed device taps
  Confirm, never one from the sender's token tree. A phone linked from the same
  token as the asking browser can confirm it. (#187)

- Server: a device or agent can ask to be allowed to approve, through the
  API or the new MCP tool `request_approve`. The change lands only when an
  allowed device taps Confirm on the prompt clrd posts. (#187)

- Server: each token, phone and browser now has an "approve" setting,
  allowed or denied. Only an allowed device can tap a reply button, and no
  token can answer a request it sent, so an agent cannot approve its own
  permission prompts. Linked phones and browsers stay allowed on upgrade;
  plain tokens start denied (`clrd token create --approve allowed` to make an
  allowed one). A device can lower its own setting but never raise it. (#187)

### Fixed

- Web: on a phone, the Linked devices table in Settings fits the screen; the
  "May approve" menu now sits under its label.
- Android: changing the sound replaces the notification channels, which
  drops a Do Not Disturb pass you set for urgent ones (Android lets only you
  set it). Settings now tells you and offers one tap to turn it back on. (#346)

- Android: a refused approval tap now says why (this phone may not answer,
  the request came from this phone's own token, or the question changed)
  and is not retried. An answer from an ask phone says it was sent for
  confirmation and the Confirm prompt shows. Settings shows whether this
  phone can answer approval requests. (#330)
- Android: the Cleared tab follows the server keep setting, so a cleared item the server deleted (globally or for its topic) leaves the phone too, and the tab says when the server holds more cleared items than the phone shows (#261).

- Web: in a topic's empty inbox, the first tip's test command sends to that
  topic, so the test shows up where you are looking. The topic icon tip only
  shows once there is a topic to set an icon on (#258).
- Android: a notification sent with `repeat` rings again on the phone each
  time it repeats, even while it is still in the tray, until someone deals
  with it. Settings has a new button that opens Android's own screen for
  letting urgent (priority 5) notifications through Do Not Disturb. (#259)
- Android: the sound you pick is saved on the server for this phone, so an
  agent can read and change it over the API or MCP and the phone follows at
  once. A sound picked before this moves to the server on the next connect,
  and one the server refuses stays on the phone and wins (#289).
- Android: dark notification icons (GitHub's and others) no longer vanish in
  the dark theme. They sit on a small light tile there, as on the web inbox
  (#313).
- Android: a silenced topic or tag no longer rings the phone. The message
  still shows in the shade and the inbox, without sound or vibration (#328).

- Server: the format check's example message, which goes into the prompt you
  paste into your agent, now shows passwords, keys and tokens in it as
  `[hidden]` (#315).
- Server: changing a device's sound answers with the sound it saved, even
  when two changes land at once, and a browser unlinked mid-request can no
  longer change one. The MCP `sound` tool now refuses an empty name like the
  API does, instead of listing the names. (#299)
- Web: a message sent without a title now shows its first line as the
  headline, as plain text, and the rest as the formatted body, instead of the
  whole message squeezed into the headline (#303).
- Web: dark site icons (GitHub's and others) no longer vanish in dark mode.
  Link, topic and notification icons sit on a small light tile there, and a
  link icon that fails to load is hidden instead of leaving a gap (#309).
- Server: behind a TLS proxy with `CLRD_BASE_URL` unset, the app link on the
  pair and link pages now says `https://` instead of `http://`, taken from the
  proxy's `X-Forwarded-Proto` (#254).
- Server: a reply button on an old card can no longer answer a question that
  was updated since, even after the update has aged out of the event log
  (#280). The device gets a conflict and shows the current question.
- Server: the "seen in the last 10 minutes" device list no longer shows a
  reader token devices from other people's sign-ins. It now shows the same
  devices as the full linked devices list (#263).
- Server: ntfy publish URLs with `tpl=yes` now render their templated
  message, title and priority from the JSON body, as ntfy does. Before, a
  templated priority was refused with "invalid priority" and the alert was
  lost, and a templated message arrived as raw JSON (#272).
- F-Droid: updating the app no longer fails with a 404 when the phone's
  F-Droid app list is a few builds old. The clrd F-Droid repo now keeps the
  last 30 builds instead of 5 (#277).
- Web: the linked devices list in Settings fits a phone screen again (the
  Linked date column hides on narrow screens; Last seen stays).
- Android: swiping a notification out of the phone's notification shade (one,
  a stack, or Clear all) no longer clears it on every device. It only hides it
  on this phone, and it stays open in the inbox. Clear it in the app, on the
  web, or with the notification's Clear button. Before, one swipe on a bundle
  could clear every notification everywhere (#265).

### Added

- Integrations: a Claude Code plugin, installed from this repo with
  `/plugin marketplace add thejdubb02/clrd`. One install and two questions
  (server URL and token) give Claude the clrd MCP server, a ping when a task
  finishes or waits on you, and permission prompts you allow or deny from
  your phone. No answer in time leaves the prompt on screen. (#156)
- Server: Gotify senders work. `POST /message` takes Gotify's app token
  (`?token=` or `X-Gotify-Key`), JSON, form or multipart body, priority
  0 to 10 and the Markdown, click and image extras, and answers in Gotify's
  shape. The app token is a clrd token limited to one topic. Apprise and the
  gotify CLI are checked; shoutrrr is not yet. (#116)

- Android: Stack by topic now also folds the app's list, as on the web.
  Two or more notifications of one topic show as one card with the count and
  the top one's title; tap it to show or hide them. Not when one topic is picked.
  (#231)
- Server: try a webhook template before pointing a service at it.
  `POST /api/v1/topics/{topic}/templates/{name}/preview` (and the MCP tool
  `preview_template`) takes a sample payload and returns the notifications a
  real post would make, without publishing anything. (#332)
- Android: Settings lists the linked devices (name, kind, what each is linked from, last seen) with an Unlink button that asks first, and an activity log of links, sign-ins and unlinks. When this phone itself is unlinked or its token revoked, the app says so and opens the sign-in screen at once (#264).

- Web: Settings shows who may approve for each linked device (Allowed, Ask
  or Denied), and an allowed browser changes it there. A browser can lower
  its own setting, never raise it. A reply tap from an Ask browser says it
  was sent for confirmation, and a refused tap says why on the card. The
  device list in the API now carries `via_id`, so names that repeat cannot
  be mixed up (#329).
- Web: Settings lists outgoing webhooks with their recent delivery log, and
  an admin can add, edit and delete them there, with or without JavaScript
  (#238).
- Web: Settings gives each topic its own "keep cleared messages" period, and
  asks first when a change deletes messages, like the global setting (#323).
- Web: starting and ending a silence in Settings works without JavaScript, and a device that cannot change silences no longer sees Start silence or End now (#326).
- Web: Settings lists the running silences with the time left, starts one
  (topic, tag or both, for a set time or until a time, with a comment) and
  ends one early (#257).
- Web: a repeating alert shows on its card how many times it alerted and when
  the next one is due, until someone acts on it (#260).
- Web: Settings shows the format check under Message format: each topic with
  hard to read messages, the count per problem, and a button to copy the fix
  prompt. Nothing shows when there is nothing to fix (#308).
- Server: give one topic its own "keep cleared messages" days with `topic_keep_cleared_days`, over the API or MCP, and preview it per topic (#262).

- Server: a format check finds topics whose messages are hard to read
  (Markdown sent as plain text, unknown shortcodes, no title, very long
  messages, raw JSON) and gives a ready prompt to fix the sender, through
  `GET /api/v1/format-check` and the MCP tool `format_check` (#308).
- Server: emoji shortcodes like `:warning:` show as the emoji on the web, in
  the Android app and tray, and in every API and stream, and a tag that names
  an emoji goes in front of the title, as in ntfy. Unknown names, code and
  times like `10:30:15` stay as typed (#306).
- Server: the device list says which devices are connected right now and
  caught up, and the last event each was sent (`connected`, `caught_up`,
  `synced_seq`). Every open WebSocket gets the new list when another device
  connects, catches up or drops (#300).
- Web: the empty inbox is friendlier. A larger "Nothing waiting" with the
  clrd mark, an icon on every tip, a new tip about sounds, and tips that move
  on by themselves every few seconds. They hold still while you hover or
  focus them, while the tab is hidden, and when your system asks for reduced
  motion (#292).

- MCP: tools to set up every feature except the environment settings (which
  `server_env` reads), so an agent needs no web page or command line: dismiss, reopen and clear; silences; topic icon and check-in;
  incoming webhook templates; and, with an admin token, outgoing webhooks,
  tokens with per-topic grants, the server settings in effect and ignored ntfy
  options (#252).
- Server: each device can have its own notification sound (default, none,
  chime, ding, pop, bell, alert or soft), read and set through the API and the
  MCP `sound` tool; `GET /api/v1/sounds` lists the names (#282).
- Android: pick the notification sound in Settings, with a Play button to
  hear each one: the phone's default (still the default), none, or one of
  the six built-in clrd sounds. Only that phone changes. If the phone's own
  notification settings override the choice, Settings says so (#282).
- Web: a sound plays when a new notification arrives while the inbox is open,
  in a desktop browser or the installed app. Pick Chime, Ding, Pop, Bell,
  Alert, Soft or None in Settings and hear it first. Chime plays with no
  setup. Silenced notifications make no sound, and if the browser blocks
  sound until you click the page, the inbox says so instead of failing
  quietly (#282).
- Server: an admin can read every server setting in effect with
  `GET /api/v1/server`, and create a token with per-topic grants over the API
  (`grants` on `POST /api/v1/tokens`), so an agent can check and set up access
  without the command line (#253).
- Server: every state change now records how it was made (`via`: the Android
  inbox, a tray button, the web inbox, an agent, ntfy, or the
  server itself), on the event and as one log line per change, so "I never
  cleared that" can be answered from the log. The Android app and the web inbox
  send it (#267, #265).
- Server: `GET /api/v1/counts` and the MCP `counts` tool give the exact number
  of notifications in each state, plus inbox and cleared totals.
- Settings lists every linked device (not only the last 10 minutes) with how
  and when each was linked, unlinks a browser or phone from another device
  after a confirm, and shows a log of links, sign-ins and unlinks. An unlinked
  browser goes to sign-in at once with a plain message. The same is on the
  API (`GET /api/v1/devices?all=1`, `GET /api/v1/devices/log`,
  `DELETE /api/v1/devices/{id}`) and MCP (`devices`, `device_log`, `unlink`).
- Keep cleared messages for 1, 5, 7, 30 (default), 90 days, forever or a
  custom number of days: set it in web Settings, over the API
  (`keep_cleared_days`) or with the new MCP `settings` tool. Settings shows how
  many messages a shorter period would delete before you confirm.

- Docs: the README and landing page now open with the out-of-box path (one
  setting, the setup link, linking the phone by QR, send), point to the clrd F-Droid repo
  for the Android app, show Docker Compose as the way to run it, and list
  stacking by topic, snooze settings, topic icons, sub-path hosting and
  user-installed certificates. Landing page examples use `clrd.example.com`.
- Server: `clrd help` lists the commands and settings.

### Changed
- Docs: plainer wording on the README and landing page. The badge row is
  gone, and the landing page no longer promises a five-minute switch from
  ntfy (#123).
- Docs: README, the landing page and the docs now match what `main` does.
  The approve setting is documented (new CLI tokens are denied, and the
  headless admin command passes `--approve allowed`), formatting covers
  emoji shortcodes,
  and the MCP tools for repeat, format check, silences, templates, template
  preview and outgoing webhooks are listed where each feature is described.
- Web: the sound picked in Settings is saved on the server for this browser, so an agent can set it over the API or MCP and an open Settings page follows. A sound picked before this moves to the server on the next visit; if the server refuses it, it stays in the browser (#297).
- Web: the sync indicator shows one dot per device, green when it is connected and red when it is not, and updates live as devices come and go. "In sync on N devices" counts only the connected ones, the indicator links to Linked devices, and Settings shows each device as Connected, Catching up or Offline (#295).

- Android: the release APK is shrunk with R8 and resource shrinking, from
  7.8 MB to 1.9 MB. CI now fails if the server binary or the APK grows past
  its size budget, and prints both sizes in the job summary.
- Web: the empty inbox sits centered in the main panel, and its tip reads as a
  quiet hint instead of a card (#302).
- SDKs and GitHub Action: no default server. The Go, JavaScript and Python
  clients used to fall back to `clrd.itsjustin.me`, which is not a clrd
  server; now a call with no URL and no `CLRD_URL` fails saying to set it, and
  the action's `url` input is required. Docs use `clrd.example.com` (#249).
- Server: runs with no settings as any user. The database defaults to
  `~/.local/share/clrd/clrd.db` (an existing `/data/clrd.db` is still used),
  the phone pairing link uses the address your browser opened clrd on when
  `CLRD_BASE_URL` is unset, and a missing token or topic error says what to
  do next. `clrd token create` no longer needs `--name`.

- Web: the empty inbox shows one tip at a time, with previous and next, a
  slow rotation that pauses on hover or focus, and copy-ready commands and AI
  agent prompts that use your own server address. A first run starts with
  sending a test.
- Server: alerts that repeat until someone acts. Publish with `"repeat": "5m"`
  (and optional `repeat_times`, default 10) and the server alerts again while
  the notification stays active; acknowledge, snooze, dismiss or resolve stops
  it on every device. Also on the MCP `send` tool. docs/repeat.md.
- Server: silences. Mute a topic, a tag, or a tag within a topic until a set
  time; notifications still arrive and sync but carry `silenced: true` so
  apps stay quiet, and ntfy apps get them at min priority. Set over the API:
  docs/silences.md.
- Server: outgoing webhooks. A topic can POST to your URL when a notification
  is dismissed, acknowledged, resolved, snoozed or replied to, signed with
  HMAC, retried with backoff and turned off with a notice after a day of
  failures. Setup: docs/integrations/webhooks-out.md.
- Run clrd under a sub-path: put the path in `CLRD_BASE_URL`
  (`https://example.com/clrd`) and the inbox, docs, API and live updates all
  work there behind your proxy.
- MCP: new tools `topics`, `get` and `update`, so an agent can see its topics,
  read one notification and edit a progress message in place. The REST API has
  the same edit as `PATCH /api/v1/notifications/{id}`. The MCP guide has setup
  for Cursor, VS Code, Windsurf, Zed, Codex CLI and Gemini CLI.
- Android: the app trusts certificate authorities you install in Android
  settings, so a server behind your own home-lab CA works over HTTPS.
- Android: notifications from the same topic stack together in the phone's
  notification shade. Turn it off in Settings with "Stack by topic".
- Web: a "Stack by topic" setting (off by default, kept per browser). With it
  on, two or more notifications from one topic show as one card with a count
  and the newest title; tap it to see them all, or clear them all at once.
- Android: items older than a day show the full date and time instead of
  "3d", and each topic chip shows how many items that topic has.
- Android: Snooze uses the snooze default from your server settings instead
  of always 1 hour, a long press on Snooze offers the other saved times, and
  Settings has a section to change the times and the default for every device.
- Web: the device list in Settings marks the browser you are using with
  "(this browser)".
- `CLRD_TRUSTED_PROXIES` setting: list your reverse proxies so only they can
  set the client address used by the wrong-password limit.
- Web: set a topic's icon in Settings, under Topic icons.
- Web: link previews. With previews on, a message's first link shows a card
  with the page's title, summary and image, fetched by your server. Settings
  has a switch for previews and a Default, On or Off choice for each topic.
- Web: links in messages show the site's icon, fetched by your server, and
  Settings has switches for Markdown formatting, links and site icons that
  apply to every device.
- Android: set the app up by scanning the Link a phone code on a signed-in
  browser. A new Scan QR code button in Settings asks before connecting to the
  scanned server, then waits for that browser to approve the phone. Both
  screens show the same 4-digit number so you approve the right phone.
- Android: approve a browser sign-in from the phone. Opening a `clrd://pair`
  link asks before signing that browser in, and refuses a link made for a
  different server than the one the app is set up with.
- Link a phone from Settings in the web inbox: show a QR code, scan it in the
  clrd app, and approve the phone by name once both screens show the same
  4-digit number. It gets this browser's access.
- Sign in a browser with your phone: the web sign-in page shows a QR code,
  you scan it with a phone that is already signed in and tap Approve, and the
  browser signs itself in with the phone's access. No token to copy or type.
- Web: messages show their Markdown formatting (bold, lists, code, quotes,
  links) and web addresses become links, following the server's
  `render_formatting` and `autolink` settings. Other HTML shows as typed and
  images in a message are never loaded.
- Approve agent actions from your phone: point Claude Code's or Codex's
  permission hook at `/mcp/approve` and each prompt arrives as a notification
  with Allow and Deny. No answer in time means deny.
- Web: Share and Copy on each card (Share only where the browser has a share
  sheet), and cards show the notification's icon and attachment: an uploaded
  image inline, any other file as a download link. An icon or file on another
  site loads from that site, which then sees the reader's IP address.
- MCP: the `send` tool takes `icon` (an image URL) and `attachment` (a link to
  a file by URL, with an optional name).
- MCP: the `send` tool takes `format` (`markdown` or `text`), so an agent can
  send plain text that is shown exactly as written.
- Android: web links in plain text messages can be tapped, just like in
  Markdown messages.
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
- Web: a Send button in the inbox opens a box to send a notification (topic,
  title, message, priority) to every device, for a quick link or note from the
  laptop to the phone.
- Web: the browser tab shows how many notifications are open, as "(3) Inbox",
  and keeps it current when another device clears one.
- Web: times read as "5 min. ago" and similar for the last day, then as a date
  and time ("Nov 18, 14:05", with the year when it is not this year). Hover a
  time to see the exact date.

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
- Webhook templates honor `X-Markdown` and `?markdown=false`, so a webhook can
  ask for its message to be shown as plain text.

### Changed

- Building clrd from source now needs Go 1.24 or later.
- Web: every page (inbox, snoozed, cleared, settings, sign-in, setup, the
  home page and the docs) uses the new design, with dark mode that follows
  the system. Clearing a notification plays one short "cleared" motion, which
  is off when the system asks for reduced motion.

- Web: the final logo. The three lines now end on one circle around the
  green disc, so every gap is the same. The README and site header use it.

### Fixed

- Web: a browser signed in with a token that is not admin, such as one
  signed in by QR from a phone, no longer says "In sync on 0 devices". It
  lists its own devices: itself, the token or phone that signed it in, and
  that token's other sessions. It still never shows anyone else's. When the
  count is unknown the header says only "In sync". (#245)
- Web: the clrd icon now shows in Ferdium, other app wrappers, bookmarks and
  home screens. The server sends `/favicon.ico`, `/apple-touch-icon.png`, PNG
  icons at 32, 192 and 512 pixels and a small web manifest, and every page
  links them, also under a sub-path base URL (#243).
- Web: the Inbox, Snoozed and Cleared counts in the sidebar are exact; they
  stopped at 500 before.

- Android: switching to another server no longer keeps the old server's
  snooze times, and the snooze times box is no longer reset while you type in
  it when another device saves them.
- Web: Settings locks the snooze and display settings while a save is on its
  way, so an edit made in that moment is no longer lost when the page reloads.
- Android: a new alert no longer shows twice in the notification drawer, once
  bundled under the silent sync notice. The sync notice also shows the real
  connection state instead of staying on Connecting after the app is opened.
- Android: the topic chips above the inbox have a solid background and a
  gap below, so cards no longer show through them while scrolling.
- ntfy publish: a header such as `Prio: high` now wins over a query parameter
  such as `?priority=low`, the same order ntfy uses.
- A phone linked from the web inbox is no longer signed out when the same
  token signs in more than 20 browsers. Phones have their own limit of 20.
- A phone linked from a browser that was itself signed in by another browser
  now stays signed in when either browser signs out, like any linked phone.
- Restoring an older backup now makes every phone and browser drop its
  local copy and resync, instead of risking missed messages. clrd keeps a
  small `clrd.db.epoch` file next to the database for this.
- Linking a phone: approving it in the last moments of the 2 minute code no
  longer leaves the browser saying Connected while the phone never connects.
- Web: a snooze time just typed in Settings can be picked as the default
  before saving; a settings change from another device no longer drops snooze
  edits not yet saved; closing the Send box mid-send and starting a new note
  no longer clears that note when the first send finishes.
- Web: an icon that fails to load is hidden instead of shown broken; Copy
  works on a page served over plain http; external image attachments show
  inline when their name or link ends in an image type.
- The server no longer serves stale data or fails writes with "database is
  locked" after a request is cancelled mid-query, for example when the web
  inbox reloads while it is still loading.
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
