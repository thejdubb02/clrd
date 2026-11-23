//! The real app, in a real web view on a virtual screen, against the fake
//! server: what the server's page can reach, where the token and the session
//! go, and where the window may navigate.
//!
//! Needs `xvfb-run`, `dbus-daemon` and `gnome-keyring-daemon`. The This
//! computer page's buttons are not pressed here; what they call is tested in
//! the core (`core/tests/device.rs`).
#![cfg(target_os = "linux")]

#[path = "../../core/tests/common/mod.rs"]
mod common;

use common::{linked, on_disk, settle, Bus, Fake, TOKEN};
use serde_json::Value;
use std::path::Path;
use std::process::{Command, Stdio};
use std::time::{Duration, Instant};

/// What the server's page tries once it is loaded in the app's window. It
/// reports what it could see and do, then tries to leave for another site.
const PROBE: &str = r#"<!doctype html><title>probe</title><script>(async () => {
const bridge = window.__TAURI_INTERNALS__;
const invoke = bridge ? bridge.invoke : () => Promise.reject('no bridge');
const r = { cookie: document.cookie, href: location.href, calls: {} };
for (const c of ['state', 'start', 'cancel', 'retry', 'unlink', 'set_close_clears', 'plugin:app|version']) {
  try { r.calls[c] = 'ran: ' + JSON.stringify(await invoke(c, { link: 'x', plainOk: true, on: true })); }
  catch (e) { r.calls[c] = 'refused: ' + e; }
}
try { window.ipc.postMessage(JSON.stringify({ cmd: 'unlink', callback: 1, error: 2, payload: {}, options: {} })); } catch (e) {}
await new Promise((done) => setTimeout(done, 500));
await fetch('/report', { method: 'POST', body: JSON.stringify(r) });
location.href = 'ELSEWHERE';
})()</script>"#;

/// Like `common::wait`, with room for a web view starting on a busy machine.
fn wait_long(what: &str, cond: impl Fn() -> bool) {
    let until = Instant::now() + Duration::from_secs(90);
    while !cond() {
        assert!(Instant::now() < until, "timed out waiting for: {what}");
        std::thread::sleep(Duration::from_millis(100));
    }
}

/// The app with its virtual screen, as one process group that ends with the
/// test: killing only `xvfb-run` would leave the screen and the app behind.
struct Group(std::process::Child);

impl Drop for Group {
    fn drop(&mut self) {
        let group = format!("-{}", self.0.id());
        let _ = Command::new("kill").args(["--", &group]).status();
        let _ = self.0.wait();
    }
}

/// The app on a virtual screen, with everything it may write under `home`.
fn app(bus: &Bus, home: &Path, args: &[&str]) -> Command {
    use std::os::unix::process::CommandExt;

    let path = std::env::var("PATH").unwrap();
    let mut cmd = Command::new("xvfb-run");
    cmd.process_group(0)
        .arg("-a")
        .arg(env!("CARGO_BIN_EXE_clrd-desktop"))
        .args(args)
        .env("DBUS_SESSION_BUS_ADDRESS", &bus.1)
        .env("PATH", format!("{}:{path}", home.join("bin").display()))
        .env("OPENED", home.join("opened"))
        .env("HOME", home)
        .env("XDG_DATA_HOME", home.join("data"))
        .env("XDG_CACHE_HOME", home.join("cache"))
        .env("XDG_CONFIG_HOME", home.join("config"))
        .env("XDG_RUNTIME_DIR", home.join("run"))
        // The web view's own sandbox needs kernel features a CI runner may
        // not allow. It is not what this test is about.
        .env("WEBKIT_DISABLE_SANDBOX_THIS_IS_DANGEROUS", "1")
        .env("WEBKIT_DISABLE_COMPOSITING_MODE", "1")
        .stdin(Stdio::null());
    cmd
}

/// The desktop's notification service: counts what it is asked to show.
struct Shown(std::sync::Arc<std::sync::atomic::AtomicU32>);

#[zbus::interface(name = "org.freedesktop.Notifications")]
impl Shown {
    fn get_capabilities(&self) -> Vec<String> {
        vec!["actions".into(), "body".into()]
    }

    #[allow(clippy::too_many_arguments)]
    fn notify(
        &self,
        _app: String,
        _replaces: u32,
        _icon: String,
        _summary: String,
        _body: String,
        _actions: Vec<String>,
        _hints: std::collections::HashMap<String, zbus::OwnedVariant>,
        _timeout: i32,
    ) -> u32 {
        self.0.fetch_add(1, std::sync::atomic::Ordering::Relaxed) + 1
    }

    fn close_notification(&self, _id: u32) {}
}

/// The switch as the app itself reads it: kept on from an earlier run, a
/// notification the user closes is cleared everywhere.
#[test]
fn closing_clears_everywhere_when_the_kept_setting_is_on() {
    const NAME: &str = "org.freedesktop.Notifications";
    const PATH: &str = "/org/freedesktop/Notifications";

    let fake = Fake::start();
    let (bus, _daemon, _keys, home) = linked(&fake);
    let shown = std::sync::Arc::new(std::sync::atomic::AtomicU32::new(0));
    let service = zbus::block_on(
        zbus::connection::Builder::address(bus.1.as_str())
            .name(NAME)
            .serve_at(PATH, Shown(shown.clone()))
            .build(),
    )
    .unwrap();
    let data = home.join("data/org.clrd.desktop");
    std::fs::create_dir_all(&data).unwrap();
    std::fs::write(data.join("close-clears"), "").unwrap();

    let _app = Group(
        app(&bus, &home, &["--hidden"])
            .stdout(Stdio::null())
            .stderr(Stdio::null())
            .spawn()
            .expect("xvfb-run is needed for this test"),
    );
    wait_long("the app follows the server", || {
        fake.requests().iter().any(|r| r.contains("/api/v1/ws"))
    });
    settle();
    fake.publish("a", serde_json::json!({}));
    wait_long("the notification shows", || {
        shown.load(std::sync::atomic::Ordering::Relaxed) == 1
    });
    // Reason 2: closed by the user.
    zbus::block_on(service.emit_signal(
        None::<&str>,
        PATH,
        NAME,
        "NotificationClosed",
        &(1u32, 2u32),
    ))
    .unwrap();
    wait_long("the clear reaches the server", || {
        !fake.dismissed().is_empty()
    });
    assert_eq!(
        fake.dismissed(),
        [("a".to_string(), "desktop_close".to_string())]
    );
}

/// The desktop's settings portal: answers with the colour scheme it holds
/// (1 is dark, 2 is light, 0 is no preference).
struct Portal(std::sync::Arc<std::sync::atomic::AtomicU32>);

#[zbus::interface(name = "org.freedesktop.portal.Settings")]
impl Portal {
    fn read(&self, _namespace: String, _key: String) -> zbus::Variant<'static> {
        let scheme = self.0.load(std::sync::atomic::Ordering::Relaxed);
        zbus::Variant::Variant(Box::new(zbus::Variant::U32(scheme)))
    }
}

/// Dark mode on the desktop is dark mode in the window, at start and when it
/// changes while the app runs. The page says what it sees each time.
#[test]
fn the_window_follows_the_desktops_light_or_dark() {
    const PATH: &str = "/org/freedesktop/portal/desktop";
    const SEES: &str = r#"<!doctype html><title>theme</title><script>
const dark = matchMedia('(prefers-color-scheme: dark)');
const say = () => fetch('/report', { method: 'POST', body: String(dark.matches) });
dark.addEventListener('change', say);
say();
</script>"#;

    let fake = Fake::start();
    fake.set_page(SEES);
    let (bus, _daemon, _keys, home) = linked(&fake);
    let scheme = std::sync::Arc::new(std::sync::atomic::AtomicU32::new(1));
    let portal = zbus::block_on(
        zbus::connection::Builder::address(bus.1.as_str())
            .name("org.freedesktop.portal.Desktop")
            .serve_at(PATH, Portal(scheme.clone()))
            .build(),
    )
    .unwrap();
    let change = |to: u32| {
        scheme.store(to, std::sync::atomic::Ordering::Relaxed);
        zbus::block_on(portal.emit_signal(
            None::<&str>,
            PATH,
            "org.freedesktop.portal.Settings",
            "SettingChanged",
            &(
                "org.freedesktop.appearance",
                "color-scheme",
                zbus::Variant::U32(to),
            ),
        ))
        .unwrap();
    };

    let _app = Group(
        app(&bus, &home, &[])
            .stdout(Stdio::null())
            .stderr(Stdio::null())
            .spawn()
            .expect("xvfb-run is needed for this test"),
    );
    wait_long("the page says what it sees", || !fake.reports().is_empty());
    assert_eq!(fake.reports(), ["true"], "dark at start");
    change(2);
    wait_long("the window goes light", || fake.reports().len() == 2);
    // Something else changing is not a change of theme. A wrong app would
    // go dark here, so it is given a second to.
    zbus::block_on(portal.emit_signal(
        None::<&str>,
        PATH,
        "org.freedesktop.portal.Settings",
        "SettingChanged",
        &(
            "org.freedesktop.appearance",
            "contrast",
            zbus::Variant::U32(1),
        ),
    ))
    .unwrap();
    // Nor is the word of some other program on the bus.
    let stranger =
        zbus::block_on(zbus::connection::Builder::address(bus.1.as_str()).build()).unwrap();
    zbus::block_on(stranger.emit_signal(
        None::<&str>,
        PATH,
        "org.freedesktop.portal.Settings",
        "SettingChanged",
        &(
            "org.freedesktop.appearance",
            "color-scheme",
            zbus::Variant::U32(1),
        ),
    ))
    .unwrap();
    std::thread::sleep(Duration::from_secs(1));
    assert_eq!(fake.reports().len(), 2, "only the portal's theme counts");
    change(1);
    wait_long("the window goes dark again", || fake.reports().len() == 3);
    assert_eq!(fake.reports(), ["true", "false", "true"]);
}

#[test]
fn the_servers_page_gets_a_session_and_nothing_else() {
    use std::os::unix::fs::PermissionsExt;

    let fake = Fake::start();
    // Another site as far as the window can tell: a different host name for
    // the same fake, so a request that gets out would be seen.
    let elsewhere = format!(
        "{}/elsewhere?a=1&b=2",
        fake.base.replace("127.0.0.1", "localhost")
    );
    fake.set_page(&PROBE.replace("ELSEWHERE", &elsewhere));
    fake.set_version("9.9.9");

    let (bus, _daemon, keys, home) = linked(&fake);
    std::fs::create_dir_all(home.join("bin")).unwrap();
    // Stands in for the system browser: records what it was asked to open.
    let opener = home.join("bin/xdg-open");
    std::fs::write(
        &opener,
        "#!/bin/sh\nprintf '%s\\n' \"$#\" \"$@\" >> \"$OPENED\"\n",
    )
    .unwrap();
    std::fs::set_permissions(&opener, std::fs::Permissions::from_mode(0o755)).unwrap();

    let log = |name: &str| std::fs::File::create(home.join(name)).unwrap();
    let app_one = Group(
        app(&bus, &home, &[])
            .stdout(log("stdout"))
            .stderr(log("stderr"))
            .spawn()
            .expect("xvfb-run is needed for this test"),
    );

    // The page only loads for a signed-in window, so a report at all means
    // the session cookie reached the web view.
    wait_long("the page reports", || {
        !fake.reports().is_empty() || fake.unlinked()
    });
    assert!(!fake.unlinked(), "the server's page unlinked this computer");
    let report: Value = serde_json::from_str(&fake.reports()[0]).unwrap();
    assert_eq!(report["href"], format!("{}/app", fake.base));
    assert_eq!(report["cookie"], "", "the page cannot read the session");
    let calls = report["calls"].as_object().unwrap();
    assert_eq!(calls.len(), 7);
    for (command, result) in calls {
        let result = result.as_str().unwrap();
        assert!(result.starts_with("refused"), "{command}: {result}");
    }

    // Leaving for another site: the window does not go, the browser does.
    wait_long("the system browser is asked", || {
        home.join("opened").exists()
    });
    settle();
    assert_eq!(
        std::fs::read_to_string(home.join("opened")).unwrap(),
        format!("1\n{elsewhere}\n"),
        "one address, as one argument"
    );
    assert!(fake.requests().iter().all(|r| !r.contains("/elsewhere")));

    // A second start finds the first, asks it to open, and leaves.
    let second = app(&bus, &home, &[]).output().unwrap();
    assert!(second.status.success());
    assert!(
        Path::new(&format!("/proc/{}", app_one.0.id())).exists(),
        "the first app keeps running"
    );

    // Nothing the page tried had any effect.
    assert!(!fake.unlinked());
    let data = home.join("data/org.clrd.desktop");
    assert!(data.join("state.json").exists(), "the app's folder is here");
    assert!(
        !data.join("close-clears").exists(),
        "the server's page turned on closing clears everywhere"
    );
    let sessions = fake.sessions();
    assert_eq!(sessions.len(), 1, "one window session: {sessions:?}");
    assert_eq!(keys.load().unwrap().unwrap().token, TOKEN);

    // The token went to the server as a header on API calls and nowhere else.
    assert!(fake.requests().iter().all(|r| !r.contains(TOKEN)));
    assert!(fake.reports().iter().all(|r| !r.contains(TOKEN)));
    for head in fake.heads() {
        let line = head.lines().next().unwrap_or_default();
        if !line.contains(" /api/") {
            assert!(
                !head.to_lowercase().contains("authorization") && !head.contains(TOKEN),
                "a page request carried the token: {line}"
            );
        }
    }
    // Not in what the app printed, and not in any file it or the web view
    // wrote. The session's value is in no file either: the web view keeps it
    // in memory, and the keychain keeps its copy encrypted.
    assert!(!on_disk(&home, TOKEN.as_bytes()));
    let value = sessions[0].split_once('=').unwrap().1;
    assert!(!on_disk(&home, value.as_bytes()));
}
