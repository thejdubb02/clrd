//! The clrd desktop app: a tray icon, a window that shows the server's own
//! `/app`, and a small bundled page for linking this computer.
//!
//! The rules this file keeps:
//! - The device token stays in the keychain and the core. It is never given
//!   to a page, put in an address or printed.
//! - The window that shows the server's page gets no commands at all (see
//!   `capabilities/device.json`). It is signed in with a session cookie of
//!   its own, set through the web view's cookie store, not through a page.
//! - That window stays on the server it is linked to. Any other address
//!   opens in the system browser instead.
#![cfg_attr(windows, windows_subsystem = "windows")]

use clrd_core::device::{Device, View};
use clrd_core::keychain::Keychain;
use clrd_core::{Engine, Error, Server, Status};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex, OnceLock};
use std::thread::JoinHandle;
use tauri::menu::{Menu, MenuBuilder, MenuItem, SubmenuBuilder};
use tauri::tray::{MouseButton, MouseButtonState, TrayIconBuilder, TrayIconEvent};
use tauri::webview::cookie::Cookie;
use tauri::webview::NewWindowResponse;
use tauri::{AppHandle, Manager, RunEvent, Url, WebviewUrl, WebviewWindowBuilder};

const SOURCE: &str = "https://github.com/thejdubb02/clrd";

/// The running app, for a second start that asks this one to open.
static APP: OnceLock<AppHandle> = OnceLock::new();

struct Shell {
    device: Device,
    /// The sync thread and its stop flag, while this computer is linked.
    sync: Mutex<Option<(Arc<AtomicBool>, JoinHandle<()>)>>,
    status: Mutex<Status>,
    /// The server's version, when it says (#580). Older servers do not.
    version: Mutex<Option<String>>,
    /// Why the window could not open, for the This computer page.
    note: Mutex<String>,
    /// Held while the main window is being made, so two clicks make one.
    opening: Mutex<()>,
}

/// One line on where things stand, for the tray and the This computer page.
fn status_line(shell: &Shell) -> String {
    let note = shell.note.lock().unwrap().clone();
    let view = shell.device.view();
    if !note.is_empty() {
        note
    } else if view.server.is_empty() {
        "Not linked".into()
    } else if view.refused {
        "The server no longer accepts this computer".into()
    } else {
        match *shell.status.lock().unwrap() {
            Status::Connected => format!("Connected to {}", view.server),
            Status::Connecting => "Connecting".into(),
            _ => "Offline, trying again".into(),
        }
    }
}

/// Opens an address in the system browser. One argument, no shell.
fn open_outside(url: &str) {
    if !(url.starts_with("https://") || url.starts_with("http://")) {
        return;
    }
    let opener = if cfg!(windows) {
        "explorer"
    } else {
        "xdg-open"
    };
    if let Ok(mut child) = std::process::Command::new(opener).arg(url).spawn() {
        std::thread::spawn(move || child.wait());
    }
}

fn same_origin(a: &Url, b: &Url) -> bool {
    a.scheme() == b.scheme()
        && a.host_str() == b.host_str()
        && a.port_or_known_default() == b.port_or_known_default()
}

fn menu(app: &AppHandle) -> tauri::Result<Menu<tauri::Wry>> {
    let shell = app.state::<Shell>();
    let quiet = |text: String| MenuItem::new(app, text, false, None::<&str>);
    let mut about = SubmenuBuilder::new(app, "About").item(&quiet(format!(
        "clrd desktop {}",
        env!("CARGO_PKG_VERSION")
    ))?);
    if shell.device.linked().is_some() {
        if let Some(v) = shell.version.lock().unwrap().as_ref() {
            about = about.item(&quiet(format!("Server {v}"))?);
        }
        about = about.text("news", "What's new");
    }
    let about = about.text("source", "Source code").build()?;
    MenuBuilder::new(app)
        .item(&quiet(status_line(&shell))?)
        .separator()
        .text("open", "Open clrd")
        .text("device", "This computer")
        .item(&about)
        .separator()
        .text("quit", "Quit")
        .build()
}

fn refresh_tray(app: &AppHandle) {
    if let (Some(tray), Ok(menu)) = (app.tray_by_id("tray"), menu(app)) {
        let _ = tray.set_menu(Some(menu));
        let _ = tray.set_tooltip(Some(format!(
            "clrd: {}",
            status_line(&app.state::<Shell>())
        )));
    }
}

fn on_menu(app: &AppHandle, id: &str) {
    match id {
        "open" => open_main(app),
        "device" => open_device(app),
        "news" => {
            if let Some(linked) = app.state::<Shell>().device.linked() {
                open_outside(&format!("{}/docs/changelog", linked.base));
            }
        }
        "source" => open_outside(SOURCE),
        "quit" => app.exit(0),
        _ => {}
    }
}

fn build_tray(app: &AppHandle) -> tauri::Result<()> {
    let mut tray = TrayIconBuilder::with_id("tray")
        .menu(&menu(app)?)
        .tooltip("clrd")
        .show_menu_on_left_click(false)
        .on_menu_event(|app, event| on_menu(app, event.id().as_ref()))
        .on_tray_icon_event(|tray, event| {
            if let TrayIconEvent::Click {
                button: MouseButton::Left,
                button_state: MouseButtonState::Up,
                ..
            } = event
            {
                open_main(tray.app_handle());
            }
        });
    if let Some(icon) = app.default_window_icon() {
        tray = tray.icon(icon.clone());
    }
    tray.build(app).map(|_| ())
}

/// Brings a window that already exists to the front.
fn raise(app: &AppHandle, label: &str) -> bool {
    let Some(window) = app.get_webview_window(label) else {
        return false;
    };
    let _ = window.unminimize();
    let _ = window.show();
    let _ = window.set_focus();
    true
}

/// The This computer page: link, unlink, and what is wrong. The only window
/// with commands, and it only ever shows the page built into the app.
fn open_device(app: &AppHandle) {
    let app = app.clone();
    // Windows are made off the event loop's thread: making one from inside an
    // event handler can hang on Windows.
    std::thread::spawn(move || {
        if raise(&app, "device") {
            return;
        }
        let _ = WebviewWindowBuilder::new(&app, "device", WebviewUrl::App("index.html".into()))
            .title("clrd: this computer")
            .inner_size(460.0, 560.0)
            .on_navigation(|url| {
                url.scheme() == "tauri" || url.host_str() == Some("tauri.localhost")
            })
            .build();
    });
}

/// The window on the server's `/app`, signed in with a fresh session. Shows
/// the This computer page instead when there is nothing to sign in to.
fn open_main(app: &AppHandle) {
    let app = app.clone();
    std::thread::spawn(move || {
        let shell = app.state::<Shell>();
        let Ok(_one) = shell.opening.try_lock() else {
            return;
        };
        if raise(&app, "main") {
            return;
        }
        let made = match (shell.device.linked(), shell.device.session()) {
            (Some(linked), Ok(cookie)) => make_main(&app, &linked.base, cookie),
            (Some(linked), Err(Error::Retry(..))) => Err(format!(
                "Could not reach {}. Open clrd again in a moment.",
                linked.base
            )),
            // Not linked, or refused: the page says which.
            _ => Err(String::new()),
        };
        let failed = made.is_err();
        *shell.note.lock().unwrap() = made.err().unwrap_or_default();
        refresh_tray(&app);
        if failed {
            open_device(&app);
        }
    });
}

fn make_main(app: &AppHandle, base: &str, set_cookie: String) -> Result<(), String> {
    let origin: Url = base
        .parse()
        .map_err(|_| format!("{base} is not an address"))?;
    let start: Url = format!("{base}/app")
        .parse()
        .map_err(|_| format!("{base} is not an address"))?;
    let mut cookie = Cookie::parse(set_cookie).map_err(|e| e.to_string())?;
    // The store needs a host to file the cookie under. No end date, so the
    // web view keeps it in memory only and never writes it to disk: the next
    // start asks for a new one. The page must not read it, whatever was sent.
    cookie.set_domain(origin.host_str().unwrap_or_default().to_string());
    cookie.unset_expires();
    cookie.set_max_age(None);
    cookie.set_http_only(true);
    if origin.scheme() == "https" {
        cookie.set_secure(true);
    }

    let here = origin.clone();
    let window = WebviewWindowBuilder::new(
        app,
        "main",
        WebviewUrl::External("about:blank".parse().unwrap()),
    )
    .title("clrd")
    .inner_size(1000.0, 720.0)
    .on_navigation(move |url| {
        let stays = url.as_str() == "about:blank" || same_origin(url, &here);
        if !stays {
            open_outside(url.as_str());
        }
        stays
    })
    .on_new_window(|url, _| {
        open_outside(url.as_str());
        NewWindowResponse::Deny
    })
    .build()
    .map_err(|e| e.to_string())?;
    let signed_in = window
        .set_cookie(cookie)
        .and_then(|()| window.navigate(start));
    if let Err(e) = signed_in {
        let _ = window.destroy();
        return Err(e.to_string());
    }
    Ok(())
}

/// Shows nothing: system notifications on this system come in a later step.
#[cfg(not(target_os = "linux"))]
struct Quiet;

#[cfg(not(target_os = "linux"))]
impl clrd_core::Sink for Quiet {
    fn show(&mut self, _: &clrd_core::Notification, _: bool) {}
    fn close(&mut self, _: &str) {}
}

#[cfg(not(target_os = "linux"))]
fn sink(_: &AppHandle, _: Arc<Server>) -> Result<Quiet, String> {
    Ok(Quiet)
}

/// System notifications, on a bus connection of their own.
#[cfg(target_os = "linux")]
fn sink(app: &AppHandle, server: Arc<Server>) -> Result<clrd_core::notify::Notifier, String> {
    let app = app.clone();
    clrd_core::instance::session_bus()
        .and_then(|conn| {
            // "Closing clears everywhere" stays off until there is a
            // settings screen to turn it on.
            let close_clears = Arc::new(AtomicBool::new(false));
            clrd_core::notify::Notifier::new(conn, server, close_clears, move |_| open_main(&app))
        })
        .map_err(|e| format!("Notifications cannot be shown: {e}"))
}

fn state_file(app: &AppHandle) -> Result<std::path::PathBuf, String> {
    let dir = app.path().app_data_dir().map_err(|e| e.to_string())?;
    std::fs::create_dir_all(&dir).map_err(|e| e.to_string())?;
    Ok(dir.join("state.json"))
}

/// Starts following the server, if this computer is linked and not already.
fn start_sync(app: &AppHandle) {
    let shell = app.state::<Shell>();
    let mut slot = shell.sync.lock().unwrap();
    let (None, Some(linked)) = (slot.as_ref(), shell.device.linked()) else {
        return;
    };
    let stop = Arc::new(AtomicBool::new(false));
    let (app, flag) = (app.clone(), stop.clone());
    let thread = std::thread::spawn(move || {
        let shell = app.state::<Shell>();
        let server = Arc::new(Server::new(&linked.base, &linked.token));
        let engine = state_file(&app).and_then(|path| {
            let sink = sink(&app, server.clone())?;
            Engine::open(path, sink).map_err(|e| format!("The saved state cannot be read: {e}"))
        });
        let engine = match engine {
            Ok(engine) => Mutex::new(engine),
            Err(e) => {
                *shell.note.lock().unwrap() = e;
                return refresh_tray(&app);
            }
        };
        let _ = clrd_core::run(&server, &engine, &flag, |status| {
            match status {
                // Only the user unlinks: a server that is being set up again
                // answers the same way, so the keychain entry stays.
                Status::AuthFailed => shell.device.set_refused(),
                Status::Connected => *shell.version.lock().unwrap() = server.version(),
                _ => {}
            }
            *shell.status.lock().unwrap() = status;
            refresh_tray(&app);
        });
    });
    *slot = Some((stop, thread));
}

/// Stops following and waits for the thread, so nothing writes the state
/// file after this returns.
fn stop_sync(app: &AppHandle) {
    let running = app.state::<Shell>().sync.lock().unwrap().take();
    if let Some((stop, thread)) = running {
        stop.store(true, Ordering::Relaxed);
        let _ = thread.join();
    }
}

// The commands run off the event loop's thread (`async`): each may wait on
// the server or the keychain, and the page must stay usable meanwhile.

/// What the This computer page shows. Asking is also what moves a waiting
/// link forward: the moment it is approved, sync starts and the window opens.
#[tauri::command(async)]
fn state(app: AppHandle) -> (View, String) {
    let shell = app.state::<Shell>();
    if shell.device.poll().is_some() {
        start_sync(&app);
        open_main(&app);
    }
    (shell.device.view(), status_line(&shell))
}

#[tauri::command(async)]
fn start(app: AppHandle, link: String, plain_ok: bool) -> Result<bool, String> {
    app.state::<Shell>().device.start(&link, plain_ok)
}

#[tauri::command(async)]
fn cancel(app: AppHandle) {
    app.state::<Shell>().device.cancel();
}

#[tauri::command(async)]
fn unlink(app: AppHandle) {
    let shell = app.state::<Shell>();
    let base = shell.device.linked().map(|l| l.base);
    stop_sync(&app);
    shell.device.unlink();
    if shell.device.linked().is_none() {
        if let Ok(path) = state_file(&app) {
            let _ = std::fs::remove_file(path);
        }
    }
    // The session is ended on the server by now; this takes its cookie out
    // of the web view too.
    if let Some(window) = app.get_webview_window("main") {
        let _ = window.destroy();
    }
    let url = base.and_then(|b| b.parse::<Url>().ok());
    if let (Some(url), Some(any)) = (url, app.webview_windows().into_values().next()) {
        for cookie in any.cookies_for_url(url).unwrap_or_default() {
            let _ = any.delete_cookie(cookie);
        }
    }
    *shell.version.lock().unwrap() = None;
    shell.note.lock().unwrap().clear();
    refresh_tray(&app);
}

/// Reads the keychain again and starts over, after something was wrong.
#[tauri::command(async)]
fn retry(app: AppHandle) {
    let shell = app.state::<Shell>();
    stop_sync(&app);
    shell.note.lock().unwrap().clear();
    shell.device.reload();
    start_sync(&app);
    refresh_tray(&app);
}

/// How this computer shows in the server's device list.
fn computer_name() -> String {
    let name = std::fs::read_to_string("/etc/hostname")
        .ok()
        .or_else(|| std::env::var("COMPUTERNAME").ok())
        .unwrap_or_default();
    let name: String = name.trim().chars().take(60).collect();
    if name.is_empty() {
        "Desktop".into()
    } else {
        name
    }
}

fn main() {
    // One app per session. A second start asks the first to open its window
    // and leaves, which is also how the window comes back on a desktop that
    // shows no tray icons.
    #[cfg(target_os = "linux")]
    let keys = match clrd_core::instance::session_bus() {
        Ok(conn) => {
            let open = || {
                if let Some(app) = APP.get() {
                    open_main(app);
                }
            };
            if let Ok(false) = clrd_core::instance::claim(&conn, open) {
                return;
            }
            Ok(Keychain::new(conn))
        }
        Err(e) => Err(format!(
            "There is no desktop session bus, so no keychain to keep the sign-in in: {e}"
        )),
    };
    // ponytail: no single-app check on Windows yet; a second start is a
    // second app. A named mutex when the Windows build is tried for real.
    #[cfg(windows)]
    let keys = Ok(Keychain::new());

    let hidden = std::env::args().any(|a| a == "--hidden");
    let app = tauri::Builder::default()
        .manage(Shell {
            device: Device::new(keys, &computer_name()),
            sync: Mutex::default(),
            status: Mutex::new(Status::Connecting),
            version: Mutex::default(),
            note: Mutex::default(),
            opening: Mutex::default(),
        })
        .invoke_handler(tauri::generate_handler![
            state, start, cancel, unlink, retry
        ])
        .setup(move |app| {
            let app = app.handle();
            let _ = APP.set(app.clone());
            let tray = build_tray(app);
            start_sync(app);
            // Started by hand, or with no tray to reach it by: show a window.
            if !hidden || tray.is_err() {
                open_main(app);
            }
            Ok(())
        })
        .build(tauri::generate_context!())
        .expect("the app starts");
    app.run(|_, event| {
        // Closing the last window leaves the tray app running. Quit (which
        // carries an exit code) still quits.
        if let RunEvent::ExitRequested {
            api, code: None, ..
        } = event
        {
            api.prevent_exit();
        }
    });
}
