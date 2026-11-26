//! The real app, in a real web view on a virtual screen, against the fake
//! server: what the server's page can reach, where the token and the session
//! go, and where the window may navigate.
//!
//! Needs `xvfb-run`, `xdotool`, `dbus-daemon` and `gnome-keyring-daemon`. Of
//! the This computer page's buttons only Unlink is pressed here; what they
//! all call is tested in the core (`core/tests/device.rs`).
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
        // The screen is picked here, so it is written down for a test that
        // presses keys on it. The first start's line is the app's screen.
        .args(["-a", "sh", "-c"])
        // A test that writes xsettingsd.conf gets a desktop that hands GTK
        // its settings while running, as GNOME and others do on X11.
        .arg(concat!(
            r#"echo "$DISPLAY $XAUTHORITY" >> "$HOME/screen"; "#,
            r#"if [ -f "$HOME/xsettingsd.conf" ]; then xsettingsd -c "$HOME/xsettingsd.conf" 2>/dev/null & echo $! > "$HOME/xsettingsd.pid"; sleep 1; fi; "#,
            r#"exec "$0" "$@""#
        ))
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
    let portal = portal(&bus, scheme.clone());
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
    let stranger = bus.connect();
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
    // No preference any more: the app takes its own choice back.
    change(0);
    wait_long("the app's dark is taken back", || fake.reports().len() == 4);
    assert_eq!(fake.reports()[3], "false");
}

#[test]
fn the_web_appearance_pick_wins_over_the_desktop() {
    // Picks dark on a light desktop, then light once dark shows, then System
    // when the test says so.
    const PICKS: &str = r#"<!doctype html><title>pick</title><script>
const dark = matchMedia('(prefers-color-scheme: dark)');
// One at a time, so the reports arrive in the order they were seen.
let sent = Promise.resolve();
const say = () => {
  const body = String(dark.matches);
  sent = sent.then(() => fetch('/report', { method: 'POST', body }));
};
let once = false;
dark.addEventListener('change', () => {
  say();
  if (dark.matches && !once) { once = true; localStorage.setItem('clrd.theme', 'light'); }
});
say();
localStorage.setItem('clrd.theme', 'dark');
setInterval(async () => {
  const page = await (await fetch(location.href)).text();
  if (page.includes('GO ' + 'SYSTEM')) localStorage.removeItem('clrd.theme');
}, 300);
</script>"#;

    let fake = Fake::start();
    fake.set_page(PICKS);
    let (bus, _daemon, _keys, home) = linked(&fake);
    let scheme = std::sync::Arc::new(std::sync::atomic::AtomicU32::new(2));
    let portal = portal(&bus, scheme.clone());
    let _app = Group(
        app(&bus, &home, &[])
            .stdout(Stdio::null())
            .stderr(Stdio::null())
            .spawn()
            .expect("xvfb-run is needed for this test"),
    );
    let kept = home.join("data/org.clrd.desktop/theme");
    wait_long("dark picked, then light", || fake.reports().len() == 3);
    assert_eq!(fake.reports(), ["false", "true", "false"]);
    wait_long("the pick is kept", || {
        std::fs::read_to_string(&kept).is_ok_and(|pick| pick == "light")
    });

    // The desktop going dark does not move a Light pick.
    scheme.store(1, std::sync::atomic::Ordering::Relaxed);
    zbus::block_on(portal.emit_signal(
        None::<&str>,
        "/org/freedesktop/portal/desktop",
        "org.freedesktop.portal.Settings",
        "SettingChanged",
        &(
            "org.freedesktop.appearance",
            "color-scheme",
            zbus::Variant::U32(1),
        ),
    ))
    .unwrap();
    std::thread::sleep(Duration::from_secs(2));
    assert_eq!(fake.reports().len(), 3, "the pick stays light");

    // System: the desktop's dark shows.
    fake.set_page(&format!("{PICKS}<!-- GO SYSTEM -->"));
    wait_long("System follows the desktop", || fake.reports().len() == 4);
    assert_eq!(fake.reports()[3], "true");
    wait_long("System is not kept", || !kept.exists());
}

/// A desktop that states no preference leaves a dark GTK setup dark.
#[test]
fn the_window_keeps_gtks_dark_when_the_desktop_has_no_preference() {
    let fake = Fake::start();
    fake.set_page(
        r#"<!doctype html><title>theme</title><script>
fetch('/report', { method: 'POST', body: String(matchMedia('(prefers-color-scheme: dark)').matches) });
</script>"#,
    );
    let (bus, _daemon, _keys, home) = linked(&fake);
    gtk_setting(&home, "gtk-application-prefer-dark-theme=1");
    let _portal = portal(&bus, Default::default());
    let _app = Group(
        app(&bus, &home, &[])
            .stdout(Stdio::null())
            .stderr(Stdio::null())
            .spawn()
            .expect("xvfb-run is needed for this test"),
    );
    wait_long("the page says what it sees", || !fake.reports().is_empty());
    assert_eq!(fake.reports(), ["true"]);
}

/// With no desktop preference and GTK set to dark, picking Light and then
/// System brings GTK's dark back (#783).
#[test]
fn system_after_light_brings_gtks_dark_back() {
    const PICKS: &str = r#"<!doctype html><title>pick</title><script>
const dark = matchMedia('(prefers-color-scheme: dark)');
let sent = Promise.resolve();
const say = () => {
  const body = String(dark.matches);
  sent = sent.then(() => fetch('/report', { method: 'POST', body }));
};
dark.addEventListener('change', say);
say();
localStorage.setItem('clrd.theme', 'light');
setInterval(async () => {
  const page = await (await fetch(location.href)).text();
  if (page.includes('GO ' + 'SYSTEM')) localStorage.removeItem('clrd.theme');
}, 300);
</script>"#;

    let fake = Fake::start();
    fake.set_page(PICKS);
    let (bus, _daemon, _keys, home) = linked(&fake);
    gtk_setting(&home, "gtk-application-prefer-dark-theme=1");
    let _portal = portal(&bus, Default::default());
    let _app = Group(
        app(&bus, &home, &[])
            .stdout(Stdio::null())
            .stderr(Stdio::null())
            .spawn()
            .expect("xvfb-run is needed for this test"),
    );
    wait_long("dark, then the Light pick", || fake.reports().len() == 2);
    assert_eq!(fake.reports(), ["true", "false"]);
    fake.set_page(&format!("{PICKS}<!-- GO SYSTEM -->"));
    wait_long("System brings GTK's dark back", || {
        fake.reports().len() == 3
    });
    assert_eq!(fake.reports()[2], "true");
    // And it stays dark: nothing turns it off again after.
    std::thread::sleep(Duration::from_secs(2));
    assert_eq!(fake.reports().len(), 3, "dark did not stay");
}

/// A pick kept from an earlier run shows from the first moment of the next.
#[test]
fn a_kept_pick_shows_after_a_restart() {
    let fake = Fake::start();
    fake.set_page(
        r#"<!doctype html><title>pick</title><script>
fetch('/report', { method: 'POST', body: String(matchMedia('(prefers-color-scheme: dark)').matches) });
localStorage.setItem('clrd.theme', 'dark');
</script>"#,
    );
    let (bus, _daemon, _keys, home) = linked(&fake);
    // A light desktop, so only the kept pick can make it dark.
    let portal = portal(
        &bus,
        std::sync::Arc::new(std::sync::atomic::AtomicU32::new(2)),
    );
    let start = || {
        Group(
            app(&bus, &home, &[])
                .stdout(Stdio::null())
                .stderr(Stdio::null())
                .spawn()
                .expect("xvfb-run is needed for this test"),
        )
    };
    let first = start();
    wait_long("the page says what it sees", || !fake.reports().is_empty());
    assert_eq!(fake.reports(), ["false"], "light at the first start");
    let kept = home.join("data/org.clrd.desktop/theme");
    wait_long("the dark pick is kept", || {
        std::fs::read_to_string(&kept).is_ok_and(|pick| pick == "dark")
    });
    drop(first);
    wait_long("the first run is gone", || {
        let reply = zbus::block_on(portal.call_method(
            Some("org.freedesktop.DBus"),
            "/org/freedesktop/DBus",
            Some("org.freedesktop.DBus"),
            "NameHasOwner",
            &("org.clrd.Desktop",),
        ))
        .unwrap();
        !reply.body().deserialize::<bool>().unwrap()
    });

    let seen = fake.reports().len();
    let _second = start();
    wait_long("the page says what it sees again", || {
        fake.reports().len() > seen
    });
    assert_eq!(fake.reports()[seen], "true", "the kept pick at start");
}

/// A GTK theme that is dark by its name ("Adwaita-dark") gives way when the
/// desktop asks for light, and comes back when it no longer does (#703).
#[test]
fn a_desktop_that_asks_for_light_wins_over_a_dark_theme_name() {
    let fake = Fake::start();
    fake.set_page(
        r#"<!doctype html><title>theme</title><script>
const dark = matchMedia('(prefers-color-scheme: dark)');
const say = () => fetch('/report', { method: 'POST', body: String(dark.matches) });
dark.addEventListener('change', say);
say();
</script>"#,
    );
    let (bus, _daemon, _keys, home) = linked(&fake);
    gtk_setting(&home, "gtk-theme-name=Adwaita-dark");
    let scheme = std::sync::Arc::new(std::sync::atomic::AtomicU32::new(2));
    let portal = portal(&bus, scheme.clone());
    let _app = Group(
        app(&bus, &home, &[])
            .stdout(Stdio::null())
            .stderr(Stdio::null())
            .spawn()
            .expect("xvfb-run is needed for this test"),
    );
    // The page may first see the theme's dark, before the app has had its
    // say, so each step waits for what the page sees last.
    let sees = |what: &str, dark: &str| {
        wait_long(what, || {
            fake.reports().last().map(String::as_str) == Some(dark)
        })
    };
    sees("light at start", "false");
    for (to, what, dark) in [
        (1, "dark when the desktop asks for dark", "true"),
        (2, "light again", "false"),
        (0, "the theme's own dark back with no preference", "true"),
    ] {
        scheme.store(to, std::sync::atomic::Ordering::Relaxed);
        zbus::block_on(portal.emit_signal(
            None::<&str>,
            "/org/freedesktop/portal/desktop",
            "org.freedesktop.portal.Settings",
            "SettingChanged",
            &(
                "org.freedesktop.appearance",
                "color-scheme",
                zbus::Variant::U32(to),
            ),
        ))
        .unwrap();
        sees(what, dark);
    }
}

/// With a desktop that hands GTK its theme while running, the theme comes
/// back from the desktop after a light switch, so one picked later shows at
/// once instead of at the next start (#756).
#[test]
fn after_a_light_switch_a_theme_picked_on_the_desktop_shows_at_once() {
    let fake = Fake::start();
    fake.set_page(
        r#"<!doctype html><title>theme</title><script>
const dark = matchMedia('(prefers-color-scheme: dark)');
const say = () => fetch('/report', { method: 'POST', body: String(dark.matches) });
dark.addEventListener('change', say);
say();
</script>"#,
    );
    let (bus, _daemon, _keys, home) = linked(&fake);
    let conf = home.join("xsettingsd.conf");
    std::fs::write(&conf, "Net/ThemeName \"Adwaita-dark\"\n").unwrap();
    let scheme = std::sync::Arc::new(std::sync::atomic::AtomicU32::new(0));
    let portal = portal(&bus, scheme.clone());
    let _app = Group(
        app(&bus, &home, &[])
            .stdout(Stdio::null())
            .stderr(Stdio::null())
            .spawn()
            .expect("xvfb-run and xsettingsd are needed for this test"),
    );
    let sees = |what: &str, dark: &str| {
        wait_long(what, || {
            fake.reports().last().map(String::as_str) == Some(dark)
        })
    };
    let ask = |to: u32| {
        scheme.store(to, std::sync::atomic::Ordering::Relaxed);
        zbus::block_on(portal.emit_signal(
            None::<&str>,
            "/org/freedesktop/portal/desktop",
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
    sees("the desktop's dark theme at start", "true");
    ask(2);
    sees("light when the desktop asks for light", "false");
    ask(0);
    sees("the desktop's dark theme back", "true");
    // A light theme picked on the desktop now.
    std::fs::write(&conf, "Net/ThemeName \"Adwaita\"\n").unwrap();
    let pid = std::fs::read_to_string(home.join("xsettingsd.pid")).unwrap();
    assert!(Command::new("kill")
        .args(["-HUP", pid.trim()])
        .status()
        .unwrap()
        .success());
    sees("the theme picked on the desktop, at once", "false");
}

/// Unlink, pressed on the This computer page itself with the inbox window
/// open beside it: the server is told, the command finishes, and the app is
/// still there after.
#[test]
fn unlink_pressed_on_the_page_unlinks_and_the_app_stays() {
    let fake = Fake::start();
    fake.set_page(
        r#"<!doctype html><title>inbox</title><script>
fetch('/report', { method: 'POST', body: 'open' });
</script>"#,
    );
    let (bus, _daemon, keys, home) = linked(&fake);
    let mut app_one = Group(
        app(&bus, &home, &[])
            .stdout(Stdio::null())
            .stderr(Stdio::null())
            .spawn()
            .expect("xvfb-run is needed for this test"),
    );
    wait_long("the inbox window opens", || !fake.reports().is_empty());
    // A second start with a link is how the page comes up from outside.
    assert!(app(&bus, &home, &["clrd://x"])
        .output()
        .unwrap()
        .status
        .success());

    let screen = std::fs::read_to_string(home.join("screen")).unwrap();
    let (display, authority) = screen.lines().next().unwrap().split_once(' ').unwrap();
    // The page is the window on top, at the screen's corner. A click on its
    // edge starts the focus at the top. Return never flips the switch or
    // sends an empty link, so presses are safe to repeat until they land.
    let press = |keys: &[&str]| {
        Command::new("xdotool")
            .env("DISPLAY", display)
            .env("XAUTHORITY", authority)
            .args(["mousemove", "3", "3", "click", "1", "key", "--delay", "200"])
            .args(keys)
            .status()
            .expect("xdotool is needed for this test");
    };
    // Past the switch to Unlink, Return, on to the Unlink that asks "sure".
    let unlink = ["Tab", "Tab", "Return", "Tab", "Return"];

    // First against a server that refuses the removal: the page then offers
    // Try again, which only works once Unlink has finished and let go.
    let before_unlink = fake.sessions();
    assert!(!before_unlink.is_empty(), "the inbox window is signed in");
    fake.refuse_unlink(true);
    wait_long("the keychain forgets the link", || {
        // No more keys once the app has asked the server: the page changes
        // under them when the unlink ends, and the next Tab and Return then
        // land on Try again. Pressed while the keychain is still empty, that
        // takes the button away for good (#737).
        let asked = fake
            .requests()
            .iter()
            .any(|r| r.starts_with("DELETE /api/v1/session") || r.contains("/devices"));
        if !asked {
            press(&unlink);
        }
        // An unlink the server has not heard of yet gets five seconds too.
        (0..50).any(|_| {
            std::thread::sleep(Duration::from_millis(100));
            keys.load().unwrap().is_none()
        })
    });
    assert!(fake.sessions().is_empty(), "the window's session is ended");
    keys.save(&clrd_core::keychain::Linked {
        base: fake.base.clone(),
        token: TOKEN.into(),
        session: String::new(),
    })
    .unwrap();
    fake.refuse_unlink(false);
    // Unlink must also take the window's cookie out of the web view (#709).
    // New sessions go to /app only from here, so a cookie left over for all
    // of / is not replaced by the next one and rides along beside it.
    fake.session_path("/app");
    let seen = fake.heads().len();
    let live = || {
        let all = fake.requests();
        all.iter().filter(|r| r.contains("/api/v1/ws")).count()
    };
    let before = live();
    let since = Instant::now();
    wait_long("Try again links this computer again", || {
        // This step has timed out under load with no trace of why (#737):
        // say what the keychain and the server saw before it does.
        if since.elapsed() > Duration::from_secs(80) {
            let all = fake.requests();
            let last = &all[all.len().saturating_sub(10)..];
            eprintln!("keychain: {:?}, last requests: {last:#?}", keys.load());
        }
        // Past the link box and its button to Try again.
        press(&["Tab", "Tab", "Tab", "Return"]);
        live() > before
    });
    // A plain start opens the inbox window again, if Try again has not. It
    // is dropped while Try again still holds the app, so ask until it opens.
    let opened = || {
        fake.heads()[seen..]
            .iter()
            .find(|h| h.starts_with("GET /app "))
            .cloned()
    };
    wait_long("the inbox window opens again", || {
        assert!(app(&bus, &home, &[]).output().unwrap().status.success());
        (0..20).any(|_| {
            std::thread::sleep(Duration::from_millis(100));
            opened().is_some()
        })
    });
    let opened = opened().unwrap();
    assert!(opened.contains("clrd_session="), "not signed in: {opened}");
    for old in &before_unlink {
        assert!(
            !opened.contains(old.as_str()),
            "the cookie from before Unlink is still in the web view: {opened}"
        );
    }
    // The page back on top, for the last Unlink.
    assert!(app(&bus, &home, &["clrd://x"])
        .output()
        .unwrap()
        .status
        .success());

    wait_long("the server is told of the unlink", || {
        press(&unlink);
        fake.unlinked()
    });
    wait_long("the keychain forgets the link again", || {
        keys.load().unwrap().is_none()
    });
    settle();
    // `xvfb-run` ends when the app does.
    assert_eq!(app_one.0.try_wait().unwrap(), None, "the app keeps running");
}

/// A settings portal on the test bus that answers with `scheme`.
/// One line of GTK's own settings file for the app's home.
fn gtk_setting(home: &Path, line: &str) {
    let gtk = home.join("config/gtk-3.0");
    std::fs::create_dir_all(&gtk).unwrap();
    std::fs::write(gtk.join("settings.ini"), format!("[Settings]\n{line}\n")).unwrap();
}

fn portal(bus: &Bus, scheme: std::sync::Arc<std::sync::atomic::AtomicU32>) -> zbus::Connection {
    zbus::block_on(
        zbus::connection::Builder::address(bus.1.as_str())
            .name("org.freedesktop.portal.Desktop")
            .serve_at("/org/freedesktop/portal/desktop", Portal(scheme))
            .build(),
    )
    .unwrap()
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

/// A custom theme's colors reach the app and are kept for the This computer
/// page (#939). Colors that would hide the page's text are refused whole,
/// since they come from the server's page.
#[test]
fn a_custom_themes_colors_are_kept_and_anything_else_refused() {
    const CUSTOM: &str = r#"<!doctype html><title>custom</title><script>
const r = document.documentElement;
const paint = body => {
  r.dataset.scheme = 'light';
  for (const [t, v] of [['--background', '#fafafa'], ['--foreground', '#101010'], ['--body', body],
    ['--border', '#cccccc'], ['--clear', '#00aa55'], ['--clear-foreground', '#000000'], ['--destructive', '#bb2200']]) r.style.setProperty(t, v);
};
paint('#f0f0f0');
setInterval(async () => {
  if ((await (await fetch(location.href)).text()).includes('GO ' + 'GOOD')) paint('#202020');
}, 300);
</script>"#;

    let fake = Fake::start();
    fake.set_page(CUSTOM);
    let (bus, _daemon, _keys, home) = linked(&fake);
    let _portal = portal(&bus, Default::default());
    let _app = Group(
        app(&bus, &home, &[])
            .stdout(Stdio::null())
            .stderr(Stdio::null())
            .spawn()
            .expect("xvfb-run is needed for this test"),
    );
    let kept = home.join("data/org.clrd.desktop/theme");
    let holds = |what: &str, want: &str| {
        wait_long(what, || {
            std::fs::read_to_string(&kept).is_ok_and(|text| text == want)
        })
    };
    holds("unreadable colors are refused, the scheme kept", "light");
    fake.set_page(&format!("{CUSTOM}<!-- GO GOOD -->"));
    holds(
        "the colors are kept",
        "light:fafafa-101010-202020-cccccc-00aa55-000000-bb2200",
    );
}
