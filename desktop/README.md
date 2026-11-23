# clrd desktop

The desktop app for Windows and Linux (#162). It is being built in steps and
is not released yet: there is nothing to install from here today.

## What is here

- `core/`: the sync client, in Rust, with no window or GUI code. It follows
  `docs/SPEC.md` section 3: it reads the event stream over a WebSocket,
  replays from its last sequence number after a drop, and starts over from
  the open list when the server sends `reset` or its epoch changes. The
  device token is sent only as an `Authorization` header, never in a URL.

Still to come, each in its own change: Linux system notifications, the tray
app with device linking and the token in the OS keychain, and the Windows
installer.

## Build and test

Needs the Rust version in `rust-toolchain.toml` (rustup installs it on first
use) and, on Linux, the OpenSSL headers (`libssl-dev` and `pkg-config` on
Debian and Ubuntu).

```sh
cd desktop
cargo test
```

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
