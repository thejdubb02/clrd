# clrd

Public open-source project: self-hosted notification server + Android app (F-Droid) + web inbox. Core feature: a dismissal on one device syncs to all devices. Public repo, so everything here is public-facing: no secrets, no em dashes, no internal hostnames.

## Decisions (2026-11-19)

- Name: clrd. License Apache-2.0.
- Server: Go, single binary, SQLite (WAL), WebSocket for live sync, web UI embedded in the binary.
- Android: Kotlin. Swipe detection via `NotificationCompat.Builder.setDeleteIntent()`. No Google Play Services dependency (F-Droid). UnifiedPush/FCM optional later.
- State model: notifications have server-owned state (active, dismissed, acknowledged, resolved, snoozed). Every change is an event with a monotonic sequence number; clients replay from their last sequence on reconnect.
- Goal: cover ntfy's features, including an ntfy-compatible publish endpoint.
- First milestone is the sync proof (two phones + web, dismiss from each, offline catch-up) before any polish.

## Rules

- CI runs only on self-hosted cezar1 runners (`runs-on: [self-hosted, cezar1]`).
- Work tracked as GitHub issues on thejdubb02/clrd.
