//! The updater's rules (#719), against a small update server on this machine.
//!
//! The files in `tests/update/` were signed by the real `cargo tauri signer`
//! with throwaway keys that were deleted afterwards: `pub` is the public half
//! of the first, `good.sig` signs `good.bin` as version 0.2.0,
//! `noversion.sig` signs it with no version, and `otherkey.sig` signs it as
//! 0.2.0 with the second key.

use std::collections::HashMap;
use std::io::{Read, Write};
use std::net::TcpListener;
use std::path::PathBuf;
use std::sync::{Arc, Mutex};

use base64::Engine as _;
use clrd_core::update::Updater;

const PUB: &str = include_str!("update/pub");
const GOOD: &[u8] = include_bytes!("update/good.bin");
const GOOD_SIG: &str = include_str!("update/good.sig");
const NO_VERSION_SIG: &str = include_str!("update/noversion.sig");
const OTHER_KEY_SIG: &str = include_str!("update/otherkey.sig");

/// Path to status and body.
type Routes = Arc<Mutex<HashMap<&'static str, (u16, Vec<u8>)>>>;

/// Serves what `routes` holds and remembers every request, headers and all.
struct Server {
    base: String,
    routes: Routes,
    seen: Arc<Mutex<Vec<String>>>,
}

impl Server {
    fn start() -> Self {
        let listener = TcpListener::bind("127.0.0.1:0").unwrap();
        let base = format!("http://{}", listener.local_addr().unwrap());
        let routes = Routes::default();
        let seen: Arc<Mutex<Vec<String>>> = Arc::default();
        let (r, s) = (routes.clone(), seen.clone());
        std::thread::spawn(move || {
            for mut stream in listener.incoming().flatten() {
                let mut head = Vec::new();
                let mut byte = [0];
                while !head.ends_with(b"\r\n\r\n") && stream.read(&mut byte).unwrap_or(0) == 1 {
                    head.push(byte[0]);
                }
                let head = String::from_utf8_lossy(&head).to_lowercase();
                let path = head.split(' ').nth(1).unwrap_or("").to_string();
                s.lock().unwrap().push(head);
                let (code, body) = r
                    .lock()
                    .unwrap()
                    .get(path.as_str())
                    .cloned()
                    .unwrap_or((404, vec![]));
                // A 302's body is where it points.
                let head = if code == 302 {
                    format!("HTTP/1.1 302 Found\r\nLocation: {}\r\nContent-Length: 0\r\nConnection: close\r\n\r\n", String::from_utf8_lossy(&body))
                } else {
                    format!(
                        "HTTP/1.1 {code} X\r\nContent-Length: {}\r\nConnection: close\r\n\r\n",
                        body.len()
                    )
                };
                let _ = stream.write_all(head.as_bytes());
                if code != 302 {
                    let _ = stream.write_all(&body);
                }
            }
        });
        Server { base, routes, seen }
    }

    fn serve(&self, path: &'static str, code: u16, body: impl Into<Vec<u8>>) {
        self.routes
            .lock()
            .unwrap()
            .insert(path, (code, body.into()));
    }

    /// The feed at `/feed`: `version`, signed by `signature`, at `/package`.
    fn feed(&self, version: &str, signature: &str) {
        let feed = serde_json::json!({
            "version": version,
            "notes": "",
            "platforms": {"test-os": {"signature": signature.trim(), "url": format!("{}/package", self.base)}},
        });
        self.serve("/feed", 200, feed.to_string());
    }

    fn paths(&self) -> Vec<String> {
        let seen = self.seen.lock().unwrap();
        seen.iter()
            .map(|head| head.split(' ').nth(1).unwrap().to_string())
            .collect()
    }

    fn updater(&self, name: &str) -> Updater {
        let dir = std::env::temp_dir().join(format!("clrd-update-{}-{name}", std::process::id()));
        let _ = std::fs::remove_dir_all(&dir);
        std::fs::create_dir_all(&dir).unwrap();
        Updater {
            url: format!("{}/feed", self.base),
            pubkey: PUB.into(),
            running: "0.1.0".into(),
            target: "test-os".into(),
            dir,
            policy: None,
        }
    }
}

fn kept(updater: &Updater) -> PathBuf {
    updater.dir.join("clrd-update.exe")
}

#[test]
fn a_signed_newer_version_is_kept_and_no_sign_in_is_sent() {
    let server = Server::start();
    server.feed("0.2.0", GOOD_SIG);
    // The download is handed on, as a file host does.
    server.serve("/package", 302, format!("{}/file", server.base));
    server.serve("/file", 200, GOOD);
    let updater = server.updater("good");

    let update = updater.check().unwrap().expect("an update");
    assert_eq!(update.version, "0.2.0");
    assert_eq!(std::fs::read(&update.file).unwrap(), GOOD);
    assert_eq!(server.paths(), ["/feed", "/package", "/file"]);
    for head in server.seen.lock().unwrap().iter() {
        assert!(
            !head.contains("authorization") && !head.contains("cookie"),
            "{head}"
        );
    }
}

/// What `package/feed.sh` made in CI is taken by the updater with the key the
/// build was given (#719). The feed's own address is not served here, so this
/// server hands out the installer from the same folder.
#[test]
#[ignore = "needs CLRD_RELEASE_DIR and CLRD_RELEASE_PUBKEY from CI"]
fn a_release_made_by_feed_sh_is_taken() {
    let dir = PathBuf::from(std::env::var("CLRD_RELEASE_DIR").unwrap());
    let mut feed: serde_json::Value =
        serde_json::from_slice(&std::fs::read(dir.join("latest.json")).unwrap()).unwrap();
    let version = feed["version"].as_str().unwrap().to_owned();
    let url = &mut feed["platforms"]["windows-x86_64"]["url"];
    let name = url.as_str().unwrap().rsplit('/').next().unwrap().to_owned();
    let installer = std::fs::read(dir.join(&name)).unwrap();
    let server = Server::start();
    // The feed exactly as feed.sh wrote it, only pointed at this server.
    *url = format!("{}/package", server.base).into();
    server.serve("/feed", 200, feed.to_string());
    server.serve("/package", 200, installer.clone());
    let mut updater = server.updater("release");
    updater.pubkey = std::env::var("CLRD_RELEASE_PUBKEY").unwrap();
    updater.target = "windows-x86_64".into();
    updater.running = "0.0.0".into();

    let update = updater.check().unwrap().expect("an update");
    assert_eq!(update.version, version);
    assert_eq!(std::fs::read(&update.file).unwrap(), installer);
}

#[test]
fn a_build_with_no_address_or_no_key_never_asks() {
    let server = Server::start();
    server.feed("0.2.0", GOOD_SIG);
    server.serve("/package", 200, GOOD);
    let mut updater = server.updater("inert");
    updater.pubkey = String::new();
    assert!(!updater.enabled());
    assert!(updater.check().unwrap().is_none());
    assert!(updater.background().is_none());
    updater.pubkey = PUB.into();
    updater.url = String::new();
    assert!(!updater.enabled());
    assert!(updater.check().unwrap().is_none());
    assert!(server.paths().is_empty());
}

#[test]
fn switched_off_the_timer_asks_nothing_and_the_button_still_works() {
    let server = Server::start();
    server.feed("0.2.0", GOOD_SIG);
    server.serve("/package", 200, GOOD);
    let updater = server.updater("off");

    assert!(updater.auto(), "on until turned off");
    updater.set_auto(true).unwrap();
    updater.set_auto(false).unwrap();
    assert!(
        updater.dir.join("no-auto-update").exists(),
        "the file a setup script makes"
    );
    assert!(updater.background().is_none());
    assert!(server.paths().is_empty());
    assert!(!kept(&updater).exists());

    assert!(updater.check().unwrap().is_some());
    updater.set_auto(true).unwrap();
    assert!(updater.background().is_some());
}

/// What the administrator set is the switch (#725): the user's file does not
/// count either way, and the user cannot change it.
#[test]
fn the_administrators_policy_wins_over_the_users_switch() {
    let server = Server::start();
    server.feed("0.2.0", GOOD_SIG);
    server.serve("/package", 200, GOOD);
    let mut updater = server.updater("policy");

    updater.policy = Some(false);
    assert!(!updater.auto());
    assert!(updater.background().is_none());
    assert!(server.paths().is_empty(), "off by policy asks nothing");
    assert!(updater.set_auto(true).is_err());

    updater.policy = None;
    updater.set_auto(false).unwrap();
    updater.policy = Some(true);
    assert!(updater.auto());
    assert!(updater.set_auto(false).is_err());
    assert!(updater.background().is_some());

    // The policy lifted: the user's own choice is as they left it.
    updater.policy = None;
    assert!(!updater.auto());
}

/// An installer that could not even start (the kept file is no program)
/// restarts nothing, so the timer may try it again. One that did start is
/// not fetched by the timer a second time.
#[cfg(unix)]
#[test]
fn an_installer_that_was_started_is_not_started_by_the_timer_again() {
    let server = Server::start();
    server.feed("0.2.0", GOOD_SIG);
    server.serve("/package", 200, GOOD);
    let updater = server.updater("loop");

    let mut update = updater.background().expect("an update");
    assert!(updater.install(&update).is_err());
    assert!(updater.background().is_some());

    // Stands in for an installer that starts.
    update.file = "/bin/true".into();
    updater.install(&update).unwrap();
    assert!(updater.background().is_none());
    assert!(updater.check().unwrap().is_some());
}

#[test]
fn the_timer_does_not_fetch_a_version_it_already_tried() {
    let server = Server::start();
    server.feed("0.2.0", GOOD_SIG);
    server.serve("/package", 200, GOOD);
    let updater = server.updater("tried");
    std::fs::write(updater.dir.join("update-tried"), "0.2.0").unwrap();

    assert!(updater.background().is_none());
    assert_eq!(server.paths(), ["/feed"]);
    assert!(updater.check().unwrap().is_some());
}

#[test]
fn the_same_or_an_older_version_is_not_downloaded() {
    let server = Server::start();
    server.feed("0.2.0", GOOD_SIG);
    server.serve("/package", 200, GOOD);
    let mut updater = server.updater("current");
    for running in ["0.2.0", "0.3.0"] {
        updater.running = running.into();
        assert!(updater.check().unwrap().is_none());
    }
    assert_eq!(server.paths(), ["/feed", "/feed"]);
    assert!(!kept(&updater).exists());
}

/// Each of these has to be refused with nothing kept to run.
fn refused(name: &str, version: &str, signature: &str, package: &[u8]) -> Vec<String> {
    let server = Server::start();
    server.feed(version, signature);
    server.serve("/package", 200, package);
    let updater = server.updater(name);
    assert!(updater.check().is_err(), "{name} was accepted");
    assert!(updater.background().is_none());
    assert!(!kept(&updater).exists());
    server.paths()
}

#[test]
fn a_changed_file_is_refused() {
    refused(
        "changed",
        "0.2.0",
        GOOD_SIG,
        b"clrd 0.2.0 test installer, with something added\n",
    );
}

#[test]
fn a_signature_from_another_key_is_refused() {
    refused("otherkey", "0.2.0", OTHER_KEY_SIG, GOOD);
}

#[test]
fn a_signature_with_no_version_is_refused_before_any_download() {
    let paths = refused("noversion", "0.2.0", NO_VERSION_SIG, GOOD);
    assert!(!paths.contains(&"/package".to_string()));
}

/// The feed is not signed: it cannot pass an old signed installer off as new.
#[test]
fn an_old_installer_named_as_a_newer_version_is_refused() {
    let paths = refused("replay", "0.3.0", GOOD_SIG, GOOD);
    assert!(!paths.contains(&"/package".to_string()));
}

/// And the version line cannot be rewritten to match: it is signed too.
#[test]
fn a_rewritten_version_in_the_signature_is_refused() {
    let b64 = base64::engine::general_purpose::STANDARD;
    let text = String::from_utf8(b64.decode(GOOD_SIG.trim()).unwrap()).unwrap();
    assert!(text.contains("version:0.2.0"));
    let forged = b64.encode(text.replace("version:0.2.0", "version:0.3.0"));
    let paths = refused("forged", "0.3.0", &forged, GOOD);
    assert!(paths.contains(&"/package".to_string()));
}

#[test]
fn an_oversized_feed_is_refused() {
    let server = Server::start();
    server.feed("0.2.0", GOOD_SIG);
    server.serve("/package", 200, GOOD);
    // A good feed, padded: only its size is wrong.
    let mut big = server.routes.lock().unwrap()["/feed"].1.clone();
    big.resize(100 * 1024, b' ');
    server.serve("/feed", 200, big);
    let updater = server.updater("big");
    assert!(updater.check().is_err());
}
