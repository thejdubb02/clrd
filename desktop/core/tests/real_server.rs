//! The same proof against a real clrd server, so the fake in `common` cannot
//! drift from the wire. Needs a built server:
//!
//! ```sh
//! go build -o /tmp/clrd ./server/cmd/clrd
//! CLRD_BIN=/tmp/clrd cargo test --test real_server -- --ignored
//! ```

mod common;

use clrd_core::link::{self, Claim, Link};
use clrd_core::{Server, Status};
use common::{state_path, wait, Client, Kill};
use serde_json::{json, Value};
use std::net::{TcpListener, TcpStream};
use std::process::Command;

fn has(log: &[String], line: &str) -> bool {
    log.iter().any(|l| l == line)
}

/// A server on a free port with an empty database: the process, its address
/// and a token made on its command line.
fn serve() -> (Kill, String, String) {
    let bin = std::env::var("CLRD_BIN").expect("set CLRD_BIN to a built clrd server");
    let db = state_path().with_file_name("clrd.db");
    let port = TcpListener::bind("127.0.0.1:0")
        .unwrap()
        .local_addr()
        .unwrap()
        .port();
    let base = format!("http://127.0.0.1:{port}");

    let out = Command::new(&bin)
        .args(["token", "create", "--name", "desktop-test"])
        .env("CLRD_DB", &db)
        .output()
        .unwrap();
    assert!(
        out.status.success(),
        "{}",
        String::from_utf8_lossy(&out.stderr)
    );
    let token = String::from_utf8(out.stdout).unwrap().trim().to_string();
    let server = Kill(
        Command::new(&bin)
            .arg("serve")
            .env("CLRD_DB", &db)
            .env("CLRD_LISTEN", format!("127.0.0.1:{port}"))
            .env("CLRD_BASE_URL", &base)
            .spawn()
            .unwrap(),
    );
    wait("server up", || {
        TcpStream::connect(("127.0.0.1", port)).is_ok()
    });
    (server, base, token)
}

#[test]
#[ignore = "needs CLRD_BIN, the path of a built clrd server"]
fn real_server() {
    let (_server, base, token) = serve();
    let path = state_path();

    let publish = |title: &str| -> String {
        let mut res = ureq::post(format!("{base}/api/v1/notifications"))
            .header("Authorization", format!("Bearer {token}"))
            .send_json(json!({"topic": "desktop-test", "title": title, "message": "m"}))
            .unwrap();
        let v: Value = res.body_mut().read_json().unwrap();
        v["id"]
            .as_str()
            .expect("publish returns the notification")
            .to_string()
    };
    let other_device = Server::new(&base, &token);

    // Live: a publish shows, a clear from elsewhere closes it.
    let mut first = Client::start(&base, &path, &token);
    wait("connected", || {
        first.last_status() == Some(Status::Connected)
    });
    let a = publish("a");
    wait("a shows", || {
        has(&first.log.log(), &format!("show {a} alert"))
    });
    let b = publish("b");
    wait("b shows", || {
        has(&first.log.log(), &format!("show {b} alert"))
    });
    other_device.dismiss(&b, "desktop_button").unwrap();
    wait("b closes", || has(&first.log.log(), &format!("close {b}")));
    first.stop().unwrap();

    // Offline catch-up: while this computer was away, a was cleared, c came
    // in, and d came and went.
    other_device.dismiss(&a, "desktop_button").unwrap();
    let c = publish("c");
    let d = publish("d");
    other_device.dismiss(&d, "desktop_button").unwrap();
    let mut second = Client::start(&base, &path, &token);
    wait("caught up", || {
        has(&second.log.log(), &format!("close {a}"))
            && has(&second.log.log(), &format!("show {c} alert"))
    });
    wait("connected", || {
        second.last_status() == Some(Status::Connected)
    });
    assert_eq!(
        second.log.log(),
        [
            format!("show {a} quiet"),
            format!("close {a}"),
            format!("show {c} alert")
        ]
    );
    second.stop().unwrap();

    // A cursor from another database: the server says reset, and what that
    // database showed comes down.
    let stale = state_path();
    let ghost = json!({"seq": 999_999, "epoch": "another-database", "open": {"ghost": {"id": "ghost", "state": "active"}}});
    std::fs::write(&stale, ghost.to_string()).unwrap();
    let third = Client::start(&base, &stale, &token);
    wait("ghost closes", || has(&third.log.log(), "close ghost"));
    wait("connected", || {
        third.last_status() == Some(Status::Connected)
    });
    assert_eq!(
        third.log.log(),
        [
            "show ghost quiet".to_string(),
            "close ghost".to_string(),
            format!("show {c} alert")
        ]
    );
    assert!(third.engine.lock().unwrap().seq() < 999_999);
}

/// What the signed-in browser does: make a code, read the claim, approve.
fn as_browser(base: &str, token: &str, method: &str, path: &str) -> Value {
    let req = ureq::http::Request::builder()
        .method(method)
        .uri(format!("{base}{path}"))
        .header("Authorization", format!("Bearer {token}"))
        .body(())
        .unwrap();
    let mut res = ureq::run(req).unwrap();
    res.body_mut().read_json().unwrap()
}

/// What a signed-in window can do with its cookie: the status of a read.
fn window(base: &str, cookie: &str) -> u16 {
    let agent: ureq::Agent = ureq::Agent::config_builder()
        .http_status_as_error(false)
        .build()
        .into();
    agent
        .get(format!("{base}/api/v1/notifications"))
        .header("Cookie", cookie.split(';').next().unwrap())
        .call()
        .unwrap()
        .status()
        .as_u16()
}

/// The same as the app does it: the link screen's steps, with the token in
/// a real keyring from the moment it exists.
#[cfg(target_os = "linux")]
#[test]
#[ignore = "needs CLRD_BIN, the path of a built clrd server"]
fn this_computer_links_through_the_keychain_and_unlinks() {
    use clrd_core::device::Device;
    use clrd_core::keychain::Keychain;
    use common::{keyring, on_disk, Bus};

    let (_server, base, browser) = serve();
    let home = state_path().with_file_name("home");
    std::fs::create_dir_all(home.join("run")).unwrap();
    let bus = Bus::start();
    let _daemon = keyring(&bus, &home);
    let keys = Keychain::new(bus.connect());
    wait("keyring up", || keys.load().is_ok());
    let device = Device::new(Ok(Keychain::new(bus.connect())), "test desktop");

    let made = as_browser(&base, &browser, "POST", "/api/v1/link");
    let url = made["url"].as_str().unwrap();
    let code = link::parse(url).unwrap().code;
    // The server is on this computer, so plain http needs no yes.
    assert_eq!(device.start(url, false, ""), Ok(true));
    let seen = as_browser(&base, &browser, "GET", &format!("/api/v1/link/{code}"));
    assert_eq!(seen["check"], device.view().check.as_str());
    assert_eq!(device.poll(), None, "not approved yet");
    assert_eq!(keys.load(), Ok(None));

    as_browser(
        &base,
        &browser,
        "POST",
        &format!("/api/v1/link/{code}/approve"),
    );
    let linked = std::cell::RefCell::new(None);
    wait("linked", || {
        let mut l = linked.borrow_mut();
        if l.is_none() {
            *l = device.poll();
        }
        l.is_some()
    });
    let linked = linked.into_inner().unwrap();
    assert_eq!(linked.base, base);
    assert_eq!(keys.load(), Ok(Some(linked.clone())));
    assert_eq!(device.view().server, base);
    assert_eq!(device.view().check, "");
    Server::new(&base, &linked.token).list_open().unwrap();

    let first = device.session().unwrap();
    let second = device.session().unwrap();
    assert_eq!(window(&base, &first), 401, "replaced, not piled up");
    assert_eq!(window(&base, &second), 200);
    assert!(!on_disk(&home, linked.token.as_bytes()));

    device.unlink();
    assert_eq!(device.view().error, "");
    assert_eq!(window(&base, &second), 401);
    assert!(Server::new(&base, &linked.token).list_open().is_err());
    assert_eq!(keys.load(), Ok(None));
}

/// Linking this computer, signing its window in, and unlinking.
#[test]
#[ignore = "needs CLRD_BIN, the path of a built clrd server"]
fn link_sign_in_unlink() {
    let (_server, base, browser) = serve();
    let as_browser = |method: &str, path: &str| as_browser(&base, &browser, method, path);
    let window = |cookie: &str| window(&base, cookie);

    let made = as_browser("POST", "/api/v1/link");
    let target = link::parse(made["url"].as_str().unwrap()).expect("the server's own link parses");
    assert_eq!(target.base, base);
    let code = target.code.clone();

    let mine = Link::start(&target, "test desktop", "").unwrap();
    assert!(matches!(mine.claim().unwrap(), Claim::Waiting));
    // Someone who copied the link too late gets nothing.
    let thief = Link::start(&target, "thief", "").unwrap();
    assert!(matches!(thief.claim().unwrap(), Claim::Gone));
    // The browser shows the number this app shows.
    let seen = as_browser("GET", &format!("/api/v1/link/{code}"));
    assert_eq!(seen["check"], mine.check());
    assert_eq!(seen["name"], "test desktop");
    assert!(matches!(mine.claim().unwrap(), Claim::Waiting));
    as_browser("POST", &format!("/api/v1/link/{code}/approve"));
    let Claim::Approved(token) = mine.claim().unwrap() else {
        panic!("approved, so the claim hands the token over");
    };
    assert!(matches!(mine.claim().unwrap(), Claim::Gone), "single use");

    // The token is a linked device's: it reads, and it shows as this device.
    let device = Server::new(&base, &token);
    device.list_open().unwrap();

    // The window's session is its own token, not the device's.
    let before = device.session("test desktop", None).unwrap();
    assert!(before.starts_with("clrd_session=") && before.contains("HttpOnly"));
    assert!(!before.contains(&token));
    assert_eq!(window(&before), 200);
    // The app opening its window again: the new session replaces the old.
    let first = device.session("test desktop", Some(&before)).unwrap();
    assert_eq!(window(&first), 200);
    assert_eq!(window(&before), 401);
    assert!(device.version().is_some());
    // Signing the window out ends that session and leaves the device linked.
    device.end_session(&first).unwrap();
    assert_eq!(window(&first), 401);
    device.list_open().unwrap();

    // Unlinking ends the device's token and the window session under it.
    let second = device.session("test desktop", None).unwrap();
    assert_eq!(window(&second), 200);
    device.unlink().unwrap();
    assert_eq!(window(&second), 401);
    assert!(device.list_open().is_err());
    assert!(device.session("test desktop", None).is_err());
}

/// Linking this computer again replaces its row instead of adding one, and
/// the row says what system it is (#770).
#[test]
#[ignore = "needs CLRD_BIN, the path of a built clrd server"]
fn linking_again_replaces_this_computers_row() {
    let (_server, base, browser) = serve();
    let as_browser = |method: &str, path: &str| as_browser(&base, &browser, method, path);
    let link = |install: &str| {
        let made = as_browser("POST", "/api/v1/link");
        let target = link::parse(made["url"].as_str().unwrap()).unwrap();
        let mine = Link::start(&target, "test desktop", install).unwrap();
        assert!(matches!(mine.claim().unwrap(), Claim::Waiting));
        as_browser("POST", &format!("/api/v1/link/{}/approve", target.code));
        let Claim::Approved(token) = mine.claim().unwrap() else {
            panic!("approved");
        };
        Server::new(&base, &token)
    };
    let rows = || {
        let all = as_browser("GET", "/api/v1/devices?all=1");
        all["devices"]
            .as_array()
            .unwrap()
            .iter()
            .filter(|d| d["name"] == "test desktop")
            .map(|d| d["detail"].as_str().unwrap_or_default().to_string())
            .collect::<Vec<_>>()
    };

    let first = link("test-install-0123456789");
    first.list_open().unwrap();
    assert_eq!(rows(), [link::detail()]);
    let second = link("test-install-0123456789");
    second.list_open().unwrap();
    assert_eq!(rows(), [link::detail()], "one row, not two");
    assert!(first.list_open().is_err(), "the old link is revoked");
    // With no id, as before: a second row.
    link("");
    assert_eq!(rows().len(), 2);
}
