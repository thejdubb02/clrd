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
