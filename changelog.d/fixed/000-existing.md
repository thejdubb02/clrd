- Web Settings no longer reloads over unsaved edits when a setting changes on another device. An edit not saved yet in any Settings form (color rules, swipe buttons, keep cleared, webhooks, themes and more, not only snooze times) is kept, and each form says the setting changed elsewhere (#883).
- Changing an outgoing hook's URL drops deliveries still queued for the old address, so they never post to the new one (#871).
- create-clrd cancels cleanly, checks invite permissions before adding hooks, and explains when a rerun keeps existing settings (#864).

- Outgoing hooks honor a rate-limited receiver's Retry-After (up to an hour) instead of retrying sooner and burning an attempt (#859).
- Saving a Slack hook again keeps its posted message ids unless the channel changes, so later state changes still edit the original message (#870).
- Automation nodes report invalid credentials and inputs more clearly, and the Node-RED trigger slows its retries when polling fails (#862).
- Web: signing in again on the same browser keeps its theme override, and saving another setting keeps your unsaved palette draft (#867).
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
