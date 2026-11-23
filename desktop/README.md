# clrd desktop

The desktop app for Windows and Linux (#162). It is being built in steps and
is not released yet: there is nothing to install from here today.

## What is here

- `core/`: the sync client, in Rust, with no window or GUI code. It follows
  `docs/SPEC.md` section 3: it reads the event stream over a WebSocket,
  replays from its last sequence number after a drop, and starts over from
  the open list when the server sends `reset` or its epoch changes. The
  device token is sent only as an `Authorization` header, never in a URL.

  It also holds everything else that touches the OS or the server: Linux
  system notifications, the device link flow, the token in the OS keychain
  and the window's sign-in.
- `app/`: the tray icon and the window, in Tauri 2, around that core. The
  window shows the server's own `/app` in the system's web view, so there is
  no bundled browser. `app/ui/` is the one page the app brings itself, "This
  computer": paste a link from Settings > Devices on the server, confirm the
  check code there, and unlink later.

Still to come, each in its own change: the Windows installer, a Linux
package, and opening `clrd://link` addresses straight from the browser.

## How the window is kept apart from the token

- The server's page runs in the window with no native commands at all. Only
  the bundled "This computer" page may call the core
  (`app/capabilities/device.json`).
- The window is signed in with a session cookie the core asks the server for.
  The app puts it into the web view's cookie store itself, HttpOnly and for
  the server's host only. It has no expiry, so the web view keeps it in
  memory and does not write it to disk; the app asks for a new one at each
  start.
- The token stays in the OS keychain (Secret Service on Linux, Credential
  Manager on Windows) and in the core's memory. It is sent only as an
  `Authorization` header on API calls.
- The window stays on the server's address. Any other link opens in the
  system browser.

## Build and test

Needs the Rust version in `rust-toolchain.toml` (rustup installs it on first
use). On Debian and Ubuntu the core needs `libssl-dev` and `pkg-config`, and
the app also needs `libwebkit2gtk-4.1-dev` and `libayatana-appindicator3-dev`.
At run time the tray icon needs `libayatana-appindicator3-1`: without it the
app stops at start.

```sh
cd desktop
cargo test
cargo run -p clrd-desktop
```

The app's own test starts the real binary on a virtual screen, so it also
needs `xvfb`, `dbus-daemon` and `gnome-keyring`.

The tests run the client against a small fake clrd server
(`core/tests/common`), so they need no network and no running server. One
more test runs the same proof against a real server, to keep the fake honest:

```sh
go build -o /tmp/clrd ./server/cmd/clrd   # from the repo root
CLRD_BIN=/tmp/clrd cargo test --test real_server -- --ignored
```

## Dependencies

The app is meant to stay small (#298), so every crate has to earn its place.
The reason for each one is written next to it in `core/Cargo.toml`.
