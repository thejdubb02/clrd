# clrd desktop

The desktop app for Windows and Linux (#162). It is being built in steps and
is not released yet: there is nothing to install from here today.

## What is here

- `core/`: the sync client, in Rust, with no window or GUI code. It follows
  `docs/SPEC.md` section 3: it reads the event stream over a WebSocket,
  replays from its last sequence number after a drop, and starts over from
  the open list when the server sends `reset` or its epoch changes. The
  device token is sent only as an `Authorization` header, never in a URL.

  It also holds everything else that touches the OS or the server: system
  notifications, the device link flow, the token in the OS keychain
  and the window's sign-in.
- `app/`: the tray icon and the window, in Tauri 2, around that core. The
  window shows the server's own `/app` in the system's web view, so there is
  no bundled browser. `app/ui/` is the one page the app brings itself, "This
  computer": paste a link from Settings > Devices on the server, confirm the
  check code there, and unlink later. It also has the "Closing clears
  everywhere" switch: off at first, so closing a system notification only
  hides it on this computer; on, a close clears it on every device.

With the package or the Windows installer installed, "Open in the clrd app"
on the server's link page opens this app with the link already in the box. A
web page can send such an address too, so the app never acts on one by
itself: it names the server and waits for you to press Link this computer,
and the check code step follows as usual. A build run from the source tree is
not registered for `clrd://` addresses; paste the link there. On Windows the
installer registers the address for the current user and removing clrd takes
it away. Under Wine the entry was seen written, kept by an update and removed
with the app; Windows opening clrd from such an address has not been seen
yet.

One copy runs per sign-in, on Linux and on Windows: starting clrd again
opens the window of the one already running. (A Windows computer that lets
one account sign in twice at once runs one copy in each.) The window follows the
system's light or dark setting on both. Neither has run on a Windows
computer yet: the one-copy check has passed under Wine, and the light or dark
part on Windows is the window library's own and has not been seen. On
Linux, a desktop that asks for light also gets light over a GTK theme that
is dark by name (such as Adwaita-dark); a theme forced with `GTK_THEME` is
left as it is. GNOME's Light style states no preference, so there the GTK
theme decides.

On Windows, notifications are toasts, with the same rules as on Linux: the
Clear button clears everywhere, closing one only hides it on this computer
unless the switch is on, and one cleared elsewhere leaves the screen and the
notification centre. Windows shows them for an installed app only, so a
build run from the source tree syncs without them. A click opens the clrd
window; the address a sender attached is never opened from a toast. Clicking
a toast left over after clrd has quit does nothing, and the next start takes
those away. Windows may also not pass on a click or the Clear button once a
toast has moved into the notification centre: if so, clear it in the window.
None of this has been seen on a Windows computer yet: the code
is built for Windows, its text handling is tested on Linux, and under Wine,
which has no toasts, only the refusal was seen: showing one fails cleanly.

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
At run time the tray icon needs `libayatana-appindicator3-1`. Without it the
app runs with no tray icon and opens its window instead; starting clrd again
brings the window back.

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

## A package for Debian and Ubuntu

There is no download yet. To make a `.deb` from your own build (needs
`dpkg-dev` and `desktop-file-utils`):

```sh
cd desktop
cargo build --release -p clrd-desktop
package/deb.sh target/release/clrd-desktop target/deb
sudo apt install ./target/deb/clrd-desktop_*.deb
```

The package brings the tray library and the web view with it as
dependencies, recommends a keychain (GNOME Keyring, unless KWallet or
KeePassXC is already there), adds clrd to the applications menu, and starts it with your
desktop session as a tray icon with no window. To stop that, turn it off in
your desktop's startup settings or remove `/etc/xdg/autostart/clrd.desktop`;
an upgrade leaves that choice alone. It is built for the system it was built
on: a package made on a newer Ubuntu will not install on an older one.

## An installer for Windows

There is no download yet, and the installer has not been run on a Windows
computer yet: so far it is only proven to build. It is built on Linux (needs
`nsis`, `lld`, `llvm` and `clang`, and fetches Microsoft's Windows SDK on
first use, which means accepting its licence):

```sh
cd desktop
rustup target add x86_64-pc-windows-msvc
cargo install --locked cargo-xwin tauri-cli
cd app
cargo tauri build --runner cargo-xwin --target x86_64-pc-windows-msvc
```

The installer lands in
`desktop/target/x86_64-pc-windows-msvc/release/bundle/nsis/`. It installs for the
current user with no administrator prompt, adds clrd to the Start menu and to
Installed apps in Windows Settings (where it can be removed), and upgrades an
older install in place. The app uses the WebView2 that comes with Windows 10
and 11; where it is missing the installer downloads it from Microsoft.

It is not code signed. Windows SmartScreen will usually say "Windows
protected your PC" when the installer is opened; More info, then Run anyway,
starts it. A computer managed by an organisation may block it outright.

Once installed, clrd starts when you sign in to Windows, as a tray icon with
no window. To stop that, turn clrd off in Windows Settings > Apps > Startup.
Removing clrd removes the entry. The app's own updates leave it as it is,
there or not; running a newer installer by hand writes it again, which does
not undo the switch in Settings. A setup script that wants no entry at all
makes an empty file named `no-autostart` in `%APPDATA%\org.clrd.desktop`
before it runs the installer: no installer writes the entry while that file
is there. Or it deletes the value `clrd` under
`HKCU\Software\Microsoft\Windows\CurrentVersion\Run` afterwards. Run under
Wine only: the entry was written, kept and not brought back by an update,
skipped with the file there and removed with the app. Windows starting clrd
from it, and the Startup switch holding after a newer installer, have not
been seen on a Windows computer.

## Updates on Windows

There is no update address yet, so a build from this tree never asks anywhere
and shows no update settings. Like the installer, the update step has not been
run on a Windows computer yet; the checks below are proven by tests on Linux.

A build is given two things when it is compiled, and both are needed:

```sh
CLRD_UPDATE_URL=https://example.com/clrd/latest.json \
CLRD_UPDATE_PUBKEY="$(cat clrd.key.pub)" \
cargo tauri build --runner cargo-xwin --target x86_64-pc-windows-msvc
```

The address serves a small JSON feed in the shape Tauri's updater uses, so
`cargo tauri signer generate` makes the key and
`cargo tauri signer sign --app-version 0.2.0 clrd_0.2.0_x64-setup.exe` signs
each installer. The version has to be given: a signature without one is
refused.

```json
{
  "version": "0.2.0",
  "platforms": {
    "windows-x86_64": {
      "signature": "<the contents of clrd_0.2.0_x64-setup.exe.sig>",
      "url": "https://example.com/clrd/clrd_0.2.0_x64-setup.exe"
    }
  }
}
```

`package/feed.sh` signs an installer and writes that feed beside it. CI runs
it on every desktop build with a key it makes and throws away, then checks
the result with the app's own updater. Run by hand on `main` (Actions,
desktop, Run workflow), it builds with the address and key from the repository's
variables `CLRD_UPDATE_URL` and `CLRD_UPDATE_PUBKEY` (the installer is served
from the same folder as the feed, so the address is the one choice), signs
with the secret
`CLRD_UPDATE_KEY` (and `CLRD_UPDATE_KEY_PASSWORD`), and keeps the installer,
its signature and `latest.json` as a workflow file for a few days. It
publishes nothing. None of these are set yet, so a run by hand stops with an
error.

- The app asks at start and every six hours. A newer installer is kept and
  run only when its signature verifies against the key in the build and was
  made for the very version the feed names, so a feed cannot pass an old
  installer off as new. Anything else is dropped and the running version
  carries on. The installer checks once more: run with no questions, it
  stops when a newer clrd is already installed.
- The installer runs with no questions, and clrd starts again in the tray
  when it has succeeded. A version whose installer was started once is not
  tried again by itself, so a failing install cannot restart the app in a
  loop. Not proven yet: what an installer that fails half way leaves behind.
  The app has quit by then, so it stays closed until it is opened again.
- Use an `https` address. The feed is not signed, so on plain `http` someone
  on the network could hold the app on an old version (an `https` address is
  never followed to plain `http`). They still could not
  make it run anything the key did not sign.
- "Update automatically" on the This computer page is on at first. Off, the
  app never asks, downloads or installs by itself; Check for updates still
  works. A setup script turns it off by making an empty file named
  `no-auto-update` in `%APPDATA%\org.clrd.desktop`, before or after the first
  start.
- An administrator sets it for every user of a computer with the number
  (DWORD) `AutoUpdate` under `HKLM\Software\Policies\clrd`: 0 is off, any
  other number is on. While it is there the switch shows that answer, is
  greyed out and cannot be changed, and the `no-auto-update` file does not
  count. Take the number away and each user's own choice is back as they left
  it. It is read at every check, so it counts without a restart. Check for
  updates still works with the policy off. Proven with Wine: the read of each
  registry state. Not proven yet: the greyed switch on a real Windows
  computer.
- The update requests carry no sign-in: the token and the window's session
  never go to the update address.
- The private key signs every update a user will run. It never belongs in
  this repository, a build log or an issue.

## Dependencies

The app is meant to stay small (#298), so every crate has to earn its place.
The reason for each one is written next to it in `core/Cargo.toml`.
