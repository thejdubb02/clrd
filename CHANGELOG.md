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

## 0.1.0 - 2026-11-23
### Added

- Web: a cleared card now slides away and the cards below close the gap, on
  every kind of clear, and Clear all goes top to bottom (#598). A card that
  arrives while you are looking slides in with one small wiggle; nothing
  plays on load, after a reconnect or for cards that came while the tab was
  in the background, and nothing moves with reduce motion on. The timings are
  in SPEC section 10.1 so every app matches.
- Android: set a sender's quiet alarm in Settings, how long it may stay
  silent and which topic the alarm goes to, like the web (#119). Each field
  saves on its own, a refused value shows the server's reason, and an open
  alarm shows on the sender.
- Web: Settings is now one page in five sections (Notifications, Inbox,
  Display, Devices, Integrations) with a search box (#575). It matches a
  setting's name, its description or keywords such as "do not disturb",
  Enter goes to the first match, and each setting has its own link such as
  `/app/settings#sound`. Webhooks no longer show to a device that cannot
  manage them, and an admin finds a link to Senders there. Without
  JavaScript every section shows and the search box is hidden.
- Android: choose the buttons each swipe on a card reveals, in Settings, up
  to 3 per side (#161). The choice is saved on your server, so every phone
  follows it; offline the app keeps the last one it saw. A token that may not
  change settings sees the choice read-only.
- Server: Web Push for browsers (#164, server side). A device can subscribe
  at `/api/v1/push`, and each notification that would ring it in the inbox
  is pushed, encrypted, even with the tab closed. VAPID keys are made on
  first start. No new dependency.
- Web: system notifications through Web Push (#164). Settings,
  Notifications turns them on for that browser; nothing asks for permission
  before you tap it. They show with clrd closed, a tap opens the inbox, and
  closing one clears nothing. Needs HTTPS. Checked in Chromium only so far.
- Web: on a touch screen, swipe a card part way to show its buttons, then
  tap one (#161). Nothing changes until you tap, a full swipe never clears,
  and swiping back hides them. Settings, Swipe picks the buttons for each
  side. Mouse and keyboard keep the buttons on the card.
- Server: settings `swipe_start` and `swipe_end` choose the buttons a swipe
  on a card reveals (#161). Defaults match today's app (Clear and Snooze to
  the left, Ack to the right); set them with the API or the MCP `settings`
  tool. The app and web inbox follow them in a later release.
- Server: an alarm when a sender goes quiet (#119, part 3). Set `expect`
  (like `25h`) on a sender, through the API or the MCP `set_sender` tool, and
  the server raises an urgent notification when that sender sends nothing for
  that long; its next message resolves it. Off unless set.
- Web: the quiet alarm on the sender Rules form (#119, part 3, #560). Set
  "Alarm if quiet for" (like `25h`) and an alarm topic on a sender's page; a
  value the server refuses shows its message next to that field. Off until set.
- Android: a "Remove cleared items" switch in Settings (#151). When on, a
  cleared item leaves the phone as soon as the server has the clear, instead
  of staying in the Cleared tab for up to 7 days. Off by default; the server keeps its copy either way.
- Android: sender rules in Settings (#545, #119 part 2). Open a sender to pause,
  mute, cap its priority or reroute its topics, and see and undo each change,
  as on the web.
- Web: sender rules on each sender's page (#119, part 2). An admin can pause, mute,
  cap the priority or reroute one sender, and see and undo each change. Nothing
  changes until a rule is set.
- Web: a Senders page for admins (#119, part 1). It lists every token, app
  and person that has sent here, with its kind, topics, count and last seen,
  and opens one sender with its details and last notifications. Works without
  JavaScript.
- Android: the same Senders list for admins (#545, #119), in Settings. Tap a
  sender for its details and last notifications. Hidden for any other token.

- Server and API: a list of senders (#119, part 1). Every service or token
  that publishes shows up on its first message, with its kind, webhook
  template, User-Agent and address, first and last seen, and a count per
  topic, at `GET /api/v1/senders` and the MCP tools `senders` and `sender`.
  Admin only, nothing to set up.
- Server and API: sender rules (#119, part 2). Pause a sender (its publishes
  are accepted and dropped), mute it, keep its priority in a range, or route
  its topics elsewhere, with `PUT /api/v1/senders/{id}` or the MCP tool
  `set_sender`. Every change is logged with who made it and can be undone
  (`sender_changes`, `undo_sender_change`). Off until set.

- Web: a question whose reply asks for text gets a text box on its card. The
  answer shows on the card once sent, and an Ask device confirm shows the
  text being confirmed (#153). Enter sends the answer and Shift+Enter starts a
  new line in it, and a new card arriving no longer takes focus off the box
  while you type (#517).
- Server and API: typed answers (#153). One reply action can set
  `"text": true`; answering it takes 1 to 1000 characters of plain text,
  kept as `reply_text` and returned by MCP `wait_for_reply` as `text`. Same
  Allowed, Ask and Denied rules as a tap.
  Nothing changes for senders that do not ask for text.
- Android: typed answers (#153). A reply action that asks for text opens a
  reply box on the card, and in the tray it is an inline reply field. The
  text is trimmed and must be 1 to 1000 characters; a refused answer says
  why and comes back in the box. An answered card shows the typed text as
  plain text. Swiping the notification still only hides it on that phone.
  Allowed, Ask and Denied rules as a tap. The Android app does not offer a
  text box yet. Nothing changes for senders that do not ask for
  text.

### Changed

- Android: a swipe on a card no longer clears it (#161). Swiping slides the
  card aside and shows round buttons (Clear and Snooze to the left, Ack to the
  right), and nothing happens until you tap one. Swipe back to close it. Long
  press the revealed Snooze for the other times. A new "Full swipe acts" switch
  in Settings, off by default, makes a swipe all the way across run the first
  button. A clear can still be undone.

### Security

- Templates: `print`, `println`, `html`, `js`, `urlquery` and an outgoing
  hook's `json` now fail when their arguments or result pass 16 KB, so a
  small template that doubles a variable can no longer build a huge string
  in memory before the 16 KB output cap applies (#492).
- Approvals: the phone now shows every field of a tool request, not only its
  command or description, one `key: value` line each, so Allow never approves
  a field you were not shown (a working directory, a scope, flags).
- Server: `CLRD_HOOK_ALLOW=off` lets the operator lock the webhook LAN allow
  list (#498). It then stays empty whatever was saved, `PUT
  /api/v1/hooks/allow` answers 403 `locked`, the MCP tool `set_hook_allow`
  fails with the same message, and `GET` shows `"locked": true`. No token can undo it, so a
  leaked admin token cannot point webhooks at services on the server's own
  network. Unset by default: nothing changes.
- Server: a page on clrd's own host now counts as clrd's own only when its
  scheme matches too, so an https page cannot act on a plain http endpoint
  (or the reverse) when clrd knows the request's scheme. Behind a proxy that
  rewrites the host or drops the port, a page from `CLRD_BASE_URL` now passes
  instead of getting 403 (#430).
- Server: the https form of an agent invite is now `<server>/agent#<code>`.
  The code sits after `#`, which is never sent, so it no longer lands in a
  proxy's access log. The page is the same for every invite and tells the
  agent to claim with the part after `#` (#463).

- Server: a grant now allows a topic only in the grant's own case. A grant for
  `home` used to also allow `HOME` and `Home`, which are different topics, so
  a token or agent invite could reach more than it named. A match in another
  case now never allows, so a deny on `secret` still blocks `SECRET`. An ntfy
  import whose rules differ in case from the topics it names now gives less
  access; rewrite those rules in the topic's own case (#458).
- Server: a user in the config file (`CLRD_CONFIG`) named like an agent no
  longer takes the agent over and hands it the file's grants. The start stops
  with a message to rename the user, and the file never removes an agent
  (#458).
- Server: the ntfy and Gotify routes no longer answer every web site. A page
  on another site gets 403 (requests, preflight and WebSocket) unless an admin
  adds its origin with `PUT /api/v1/ntfy/origins` or the `set_ntfy_origins`
  MCP tool (`*` restores ntfy's open behaviour). Phones, scripts and the ntfy
  CLI send no Origin and work as before, as do clrd's own pages. If you use a
  hosted web app such as `https://ntfy.sh/app` against this server, add its
  origin (#374).
- Claude Code plugin: the MCP connection now refuses a server URL that is not
  https (except localhost) with a clear message, so the token never goes out
  over plain http. Before, only the hooks checked (#363).
- Android: an approval (Allow, Deny) whose text is too long for the tray to
  show in full no longer has its answer buttons in the tray. A tap opens the
  app, and the opened card shows the whole text with the answer buttons.
  Before, the app had no answer buttons at all. (#366)
- Server: a new token made through the API or the MCP tool `create_token`
  now always starts denied for approval requests, whoever makes it. Raise it
  the same way as any device, from an allowed device or with an approve
  request. Asking for `allowed` or `ask` at creation is refused. (#187)

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

- Server: a notification sent to chosen devices now lists as silenced on
  every other device too, so a phone or desktop rebuilding its inbox after a
  reset no longer alerts for one meant for another device (#588).
- Claude Code plugin: the approve hook removes its own record of a waiting
  phone prompt however it ends, so a later on-screen answer can no longer
  stop another request that reused its process id, and clearing the phone
  after an on-screen answer no longer needs `ps -o` (missing on Git Bash)
  (#420).
- Claude Code plugin: the MCP bridge reads a request's id at the top level
  only, so spaces or escapes around the id still get an answer, an `id` inside
  a notification's params no longer gets an error back, and a stuck request
  can no longer hold up other answers (#414).
- Server: in the format check by sender, messages with no known sender now
  group under `sender_id` `"unknown"`, so a finding there can be dismissed for
  that group alone instead of for every sender on the topic (#391).
- Server: a sender's detail still lists its older stored notifications when
  its newest ones were cleaned up (#530), and an anonymous sender with a change
  history is no longer dropped from the address list, so its changes can still
  be undone (#537).
- Server: revoking a token resolves its open quiet sender alarm instead of
  leaving it to be cleared by hand (#558).
- Server: a sender route that copies an upload to several topics no longer
  loses the later copies when the first copy is replaced right away (#536).
- Android: a late repeat no longer rings a notification again after Clear
  and Undo when this phone also sent it, cleared it and undid it (#550).
- Android: text typed in a reply box is kept as a draft when the box closes
  without Send or Cancel (the question was edited, back was pressed, or the
  phone turned),
  instead of being lost (#539).
- Android: the short "Sent" message after a send from the Send box shows
  again if the phone turns while it is on screen, instead of being lost (#532).
- Android: a repeat sent before you cleared a notification no longer rings
  it again after you tap Undo, when the repeat reaches the phone late (#504).
- Web: saving sender rules from a page opened before someone else changed them
  is now refused, so it can no longer undo the newer change. Reload and save
  again. The API takes the same check as an optional `base_seq`. (#543)
- Android: snooze and Undo (#508). A snooze tapped offline now ends at the
  time the phone showed, not an hour after it reconnects, so Snooze, Clear,
  Undo while offline lands the same on every device. Undo of a Clear no longer sends a second snooze that could
  overwrite an acknowledgment from another device. Any snooze time the server
  takes, such as `1.5h` or `500ms`, now shows its end at once.
- Android: typed answers (#516). A reply box left open while an earlier
  answer is refused because the question changed now closes, keeping its
  text for the next box. When the server refuses a typed answer, the tray
  says the text waits in the app, since a tray reply starts empty.
- Android: turning the phone while the Send box is sending no longer lets a
  second tap send the notification twice, and the result (sent, or the
  server's reason) still shows after the turn (#523).
- Server: a request whose declared length is over 1 MB gets 413 at once,
  before reading the body, instead of 408 when the upload is slow (#507).
- Server: shutdown now waits for background jobs (webhooks out, snooze
  wake, expiry, repeats, check-ins, prune) to stop before the database
  closes, so a job mid-write is no longer cut short (#486).
- Server: two admins saving the ntfy origins list at the same moment can no
  longer leave the server using a different list from the one saved (#497).
- Undo after Clear on a snoozed card now puts it back snoozed in one step, so
  other devices no longer show it active for a moment (#473). `reopen` takes
  an optional `until`. On Android a snooze also shows its time in the Snoozed
  list at once, not only after the server answers.
- Android: a repeat that reaches the phone just after you mark the same card
  now still rings, unless the card was acknowledged or cleared since.
- Server: a webhook turned off as shutdown begins still posts its notice, and a
  send that finishes after its webhook was deleted, recreated or turned off no
  longer clears the new webhook's failure record (#240).
- Web: a long press on Snooze on a busy or slow page now opens the snooze
  times instead of snoozing the card (#476).
- Server: a token revoked while its approval request or reply tap was in
  flight now gets 401 instead of 500 (#349).
- MCP: a whole number past 2^53 in a tool argument, like a payload id given to
  `preview_template`, now reaches the server with its exact digits (#355).
- Server: a reply tap on a question that was only marked or re-alerted after
  its older events were pruned is no longer refused with 409 (#452).
- Server: a publish still in flight when an admin turns a topic's
  `device_actions` off no longer keeps its `http` or `broadcast` action, stored
  or live (#447).
- Server: a live stream that was still starting when a person's grants
  changed now follows the new grants, and an MCP `wait_for_reply` that times
  out shows the notification only while the token can still read it (#428).
- ntfy: the short form of `X-Actions` reads `\"` inside a quoted value as a
  quote, and keeps spaces inside quotes, as ntfy does. Before, `body="a\",b"`
  split at the comma and `' a '` lost its spaces (#446).
- Server: a user in `CLRD_CONFIG` that was made on the API is no longer
  deleted when the file drops it, and a dropped user that a token made on
  the API still uses no longer stops the server from starting. Both are kept
  as they are, with a warning in the log (#440).
- Android: Undo after Clear puts a snoozed message back in Snoozed until its
  old time, instead of in the Inbox. Offline, an Ack queued before a stack's
  Clear no longer splits that Clear into one request per message (#425).
- Pushover: `attachment_base64` sent as a multipart form field takes a file
  up to the attachment cap. Before, a file over about 8 MiB sent that way
  was refused (#437).
- ntfy: a subscriber whose `since=` was pruned while it caught up no longer
  misses a live-only (`Cache: no`) message sent in that moment. The stream
  now starts over from the oldest message left without closing its live
  feed (#443).
- Android: the "May approve" menu no longer shows on a phone whose token is
  limited to some topics, where every change was refused. The server now
  marks each caller's own device row with `can_approve`, set as the web
  inbox's check (#433).
- A Pushover image sent as `attachment_base64` near the attachment cap is
  accepted. Before, base64 made the request a third bigger than the file, so
  it was refused as too large although the file itself fit (#404).
- An ntfy subscription with `?scheduled=1` no longer shows a delayed message
  sent just after the stream opened as if it were due now. Only messages that
  were already waiting when you subscribed are listed early; new delayed ones
  arrive when they fire (#432).
- `clrd import ntfy` refuses a database that already has notifications, as the
  Gotify import does. Before, importing into a server in use could let the
  next cleanup delete recent live events, so phones offline at the time
  missed them (#388).
- Claude Code plugin: the finished ping now carries Claude's last message, even past 4 KB. Before, it said only "Task finished." (#365)
- Claude Code plugin: a permission prompt answered on screen now leaves the phone too. Before, Allow on screen left the phone's prompt ringing for about 9.5 minutes. (#364)
- Web: scripts and stylesheets are served at URLs named by their content and
  cached for a year, and pages are revalidated, so after an upgrade a page never
  runs with an older cached script (#410).
- Web: a page refresh already on its way when another change lands is dropped, so a card cleared on another device no longer flashes back for a moment (and a closed stack no longer shows the old count and newest title). This was the flaky inbox browser test on main. (#402)
- Web: while a stack's Clear all waits for its reply, a page refresh already on its way no longer reshapes the stacks first, and a card another device clears inside a different closed stack updates that stack's count at once (#397).

- Server: an ntfy import replaces anonymous access set before it through the
  admin API, so no older anonymous token stays live beside the imported one
  (#375).
- Web: Clear all on stacks stays right when the server is slow to answer: a
  stack fades as one instead of shrinking card by card, a topic that arrives
  meanwhile is no longer shown as Cleared, and another stack's count updates
  at once while one fades. (#387)
- Webhook templates: saving an Edit with the secret left empty can no longer bring back an old secret that was rotated or removed at the same moment, and a field saved with a line break through the API keeps it when edited in Settings. (#376)
- Web: J and K now stop on a closed stack and Enter opens it. A notification
  another device clears inside a closed stack updates its count and newest
  title at once, and Clear all takes every stack with it. (#227)

- Settings: choosing Default for a topic's keep now counts what it would delete against the exact server keep, not whole days, and By topic lists a topic with its own keep even after its last notification is gone. `GET /api/v1/counts?keep_cleared_days=default&topic=T` (and the MCP `counts` tool) previews that. (#336)
- Android: a refused approval tap that has to be sent again shows its message
  once, not twice, and the Linked devices list in Settings updates as soon as a
  device connects, drops or has its approve setting changed.
- Web: on a phone, the Linked devices table in Settings fits the screen; the
  "May approve" menu now sits under its label.
- Android: changing the sound replaces the notification channels, which
  drops a Do Not Disturb pass you set for urgent ones (Android lets only you
  set it). Settings now tells you and offers one tap to turn it back on. (#346)
  When the sound is changed from the web inbox, the API or an agent, a
  notification on the phone says so too, and a tap on it opens the switch. (#380)

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
