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
use common::{state_path, wait, Client};
use serde_json::{json, Value};
use std::net::{TcpListener, TcpStream};
use std::process::{Child, Command};

struct Kill(Child);

impl Drop for Kill {
    fn drop(&mut self) {
        let _ = self.0.kill();
        let _ = self.0.wait();
    }
}

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

/// Linking this computer, signing its window in, and unlinking.
#[test]
#[ignore = "needs CLRD_BIN, the path of a built clrd server"]
fn link_sign_in_unlink() {
    let (_server, base, browser) = serve();
    // What the signed-in browser does: make a code, read the claim, approve.
    let as_browser = |method: &str, path: &str| -> Value {
        let req = ureq::http::Request::builder()
            .method(method)
            .uri(format!("{base}{path}"))
            .header("Authorization", format!("Bearer {browser}"))
            .body(())
            .unwrap();
        let mut res = ureq::run(req).unwrap();
        res.body_mut().read_json().unwrap()
    };
    // What a signed-in window can do with its cookie: the status of a read.
    let window = |cookie: &str| -> u16 {
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
    };

    let made = as_browser("POST", "/api/v1/link");
    let target = link::parse(made["url"].as_str().unwrap()).expect("the server's own link parses");
    assert_eq!(target.base, base);
    let code = target.code.clone();

    let mine = Link::start(&target, "test desktop").unwrap();
    assert!(matches!(mine.claim().unwrap(), Claim::Waiting));
    // Someone who copied the link too late gets nothing.
    let thief = Link::start(&target, "thief").unwrap();
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
    let first = device.session("test desktop").unwrap();
    assert!(first.starts_with("clrd_session=") && first.contains("HttpOnly"));
    assert!(!first.contains(&token));
    assert_eq!(window(&first), 200);
    // Signing the window out ends that session and leaves the device linked.
    device.end_session(&first).unwrap();
    assert_eq!(window(&first), 401);
    device.list_open().unwrap();

    // Unlinking ends the device's token and the window session under it.
    let second = device.session("test desktop").unwrap();
    assert_eq!(window(&second), 200);
    device.unlink().unwrap();
    assert_eq!(window(&second), 401);
    assert!(device.list_open().is_err());
    assert!(device.session("test desktop").is_err());
}
