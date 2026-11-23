//! The same proof against a real clrd server, so the fake in `common` cannot
//! drift from the wire. Needs a built server:
//!
//! ```sh
//! go build -o /tmp/clrd ./server/cmd/clrd
//! CLRD_BIN=/tmp/clrd cargo test --test real_server -- --ignored
//! ```

mod common;

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

#[test]
#[ignore = "needs CLRD_BIN, the path of a built clrd server"]
fn real_server() {
    let bin = std::env::var("CLRD_BIN").expect("set CLRD_BIN to a built clrd server");
    let path = state_path();
    let db = path.with_file_name("clrd.db");
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
    let _server = Kill(
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
