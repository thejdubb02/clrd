# clrd

Public open-source project: self-hosted notification server + Android app (F-Droid) + web inbox. Core feature: a dismissal on one device syncs to all devices. Public repo, so everything here is public-facing: no secrets, no em dashes, no internal hostnames.

## Decisions (2026-11-19)

- Name: clrd. License Apache-2.0.
- Server: Go, single binary, SQLite (WAL), WebSocket for live sync, web UI embedded in the binary.
- Android: Kotlin. A swipe in the notification shade only hides it on that phone (Justin, 2026-11-20, #265); clearing happens in the app, web or API. No Google Play Services dependency (F-Droid). UnifiedPush/FCM optional later.
- State model: notifications have server-owned state (active, dismissed, acknowledged, resolved, snoozed). Every change is an event with a monotonic sequence number; clients replay from their last sequence on reconnect.
- Goal: cover ntfy's features, including an ntfy-compatible publish endpoint.
- First milestone is the sync proof (two phones + web, dismiss from each, offline catch-up) before any polish.

## Rules

- Works out of the box (Justin, #242): every new feature is off or safe by
  default with zero config, and an agent can set it up through the API or MCP.
- Smaller and lighter than ntfy and the rest (Justin, #298): weigh size in
  every change. No new dependency when a few lines or the platform covers it;
  keep bundled assets small. CI fails when the server binary or the release
  APK grows past its budget in `.github/size-budget`; raising one is a
  reviewed one-line change.

- CI runs on Ubicloud standard runners (`runs-on: ubicloud-standard-4`), never premium ones (Justin, 2026-11-23). Android and F-Droid jobs stay on the self-hosted cezar1 runner, which has the Android SDK installed.
- Local Android and Go builds and test runs go to cezar1, never the devbox (its CPU is throttled by the host).
- Work tracked as GitHub issues on thejdubb02/clrd.
- Keep the repo page and site true (Justin, 2026-11-19). Any PR a user would
  notice updates, in the same PR: `CHANGELOG.md` under Unreleased (CI fails
  without it; label `no-changelog` for tests, CI and refactors), plus
  `README.md`, `web/templates/landing.html` and the matching `docs/` page when
  a headline feature, install step or integration changes. The site serves
  `docs/` and the landing page from the binary, so the repo is the site.
- README claims only what works on `main` today. No install command for a
  package or image that is not published.

## The contract

`docs/SPEC.md` is the shared contract (API, sync, states, auth, MCP, brand
tokens, directory ownership). Read it first. Hosts (Justin, 2026-11-19): our own
running server is `clrd.willhitestrategy.org` (and it takes over
`ntfy.willhitestrategy.org` at the ntfy cutover, #5). `clrd.itsjustin.me` is a
separate marketing site for Justin's portfolio, built outside this repo; never
point it at a clrd server. Parallel threads each own one area (server, web,
android, integrations); stay in yours and open a PR to `main`.
