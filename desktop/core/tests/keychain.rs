//! The keychain against a real Secret Service (GNOME Keyring) on a private
//! bus, with a home folder of its own. Nothing here touches a real keyring.
#![cfg(target_os = "linux")]

mod common;

use clrd_core::keychain::{Keychain, Linked};
use common::{keyring, on_disk, state_path, wait, Bus};

const TOKEN: &str = "clrd_keychaintesttoken000000000000000";
const SESSION: &str = "clrd_session=clrd_keychaintestsession0000000; Path=/; HttpOnly";

fn linked(base: &str, token: &str) -> Linked {
    Linked {
        base: base.into(),
        token: token.into(),
        session: SESSION.into(),
    }
}

#[test]
fn the_token_is_kept_by_the_keyring_across_restarts() {
    let home = state_path().with_file_name("home");
    std::fs::create_dir_all(home.join("run")).unwrap();
    let bus = Bus::start();
    let daemon = keyring(&bus, &home);
    let keys = Keychain::new(bus.connect());
    wait("keyring up", || keys.load().is_ok());

    assert_eq!(keys.load(), Ok(None));
    keys.save(&linked("https://old.example.com", "clrd_old"))
        .unwrap();
    let mine = linked("https://clrd.example.com", TOKEN);
    keys.save(&mine).unwrap();
    assert_eq!(keys.load(), Ok(Some(mine.clone())));

    // After a reboot: a new keyring process and a new connection read the
    // one entry back, so it was kept and not only held in memory.
    drop(daemon);
    wait("keyring down", || keys.load().is_err());
    let _daemon = keyring(&bus, &home);
    let again = Keychain::new(bus.connect());
    wait("keyring back", || again.load().is_ok());
    assert_eq!(again.load(), Ok(Some(mine)));
    // What the keyring wrote holds neither secret in the clear. This line
    // checks the keyring, not this app, which writes no file at all.
    assert!(!on_disk(&home, TOKEN.as_bytes()));
    assert!(!on_disk(&home, b"clrd_keychaintestsession"));

    again.clear().unwrap();
    assert_eq!(again.load(), Ok(None));
}

#[test]
fn with_no_keychain_nothing_is_stored_and_it_says_so() {
    let bus = Bus::start();
    let keys = Keychain::new(bus.connect());
    assert!(keys
        .save(&linked("https://clrd.example.com", TOKEN))
        .is_err());
    assert!(keys.load().is_err());
    assert!(keys.clear().is_err());
}

/// A keyring that only opens and unlocks.
struct Service;

#[zbus::interface(name = "org.freedesktop.Secret.Service")]
impl Service {
    fn open_session(
        &self,
        _algorithm: String,
        _input: zbus::OwnedVariant,
    ) -> (zbus::Variant<'static>, zbus::OwnedObjectPath) {
        ("".into(), "/session".try_into().unwrap())
    }

    fn unlock(
        &self,
        objects: Vec<zbus::OwnedObjectPath>,
    ) -> (Vec<zbus::OwnedObjectPath>, zbus::OwnedObjectPath) {
        (objects, "/".try_into().unwrap())
    }
}

/// Its searches name an entry that is not there: for the first `0` searches,
/// then none.
struct Ghost(std::sync::atomic::AtomicU32);

#[zbus::interface(name = "org.freedesktop.Secret.Collection")]
impl Ghost {
    fn search_items(
        &self,
        _attributes: std::collections::HashMap<String, String>,
    ) -> Vec<zbus::OwnedObjectPath> {
        use std::sync::atomic::Ordering::Relaxed;
        let left = self.0.load(Relaxed);
        if left == 0 {
            return vec![];
        }
        self.0.store(left - 1, Relaxed);
        vec!["/org/freedesktop/secrets/collection/login/2"
            .try_into()
            .unwrap()]
    }
}

fn ghost(bus: &Bus, searches: u32) -> zbus::Connection {
    zbus::block_on(
        zbus::connection::Builder::address(bus.1.as_str())
            .name("org.freedesktop.secrets")
            .serve_at("/org/freedesktop/secrets", Service)
            .serve_at(
                "/org/freedesktop/secrets/aliases/default",
                Ghost(searches.into()),
            )
            .build(),
    )
    .unwrap()
}

/// Another process unlinks between this one's search and its read (#733):
/// that reads as not linked, not as a broken keychain.
#[test]
fn an_entry_removed_under_a_read_is_nothing() {
    let bus = Bus::start();
    let _service = ghost(&bus, 1);
    assert_eq!(Keychain::new(bus.connect()).load(), Ok(None));
}

/// An entry that is still listed and cannot be read stays an error.
#[test]
fn an_entry_that_stays_and_cannot_be_read_is_an_error() {
    let bus = Bus::start();
    let _service = ghost(&bus, 2);
    assert!(Keychain::new(bus.connect()).load().is_err());
}

/// What a fake keyring was asked, in order.
type Log = std::sync::Arc<std::sync::Mutex<Vec<String>>>;

const ENTRY: &str = "/org/freedesktop/secrets/collection/login/1";

/// A keyring that locks its one entry by itself, not only the whole
/// keyring. With `ask`, every unlock asks the user in a dialog.
struct Locked {
    log: Log,
    ask: bool,
}

#[zbus::interface(name = "org.freedesktop.Secret.Service")]
impl Locked {
    fn open_session(
        &self,
        _algorithm: String,
        _input: zbus::OwnedVariant,
    ) -> (zbus::Variant<'static>, zbus::OwnedObjectPath) {
        self.log.lock().unwrap().push("open".into());
        ("".into(), "/session".try_into().unwrap())
    }

    fn unlock(
        &self,
        objects: Vec<zbus::OwnedObjectPath>,
    ) -> (Vec<zbus::OwnedObjectPath>, zbus::OwnedObjectPath) {
        let names: Vec<&str> = objects.iter().map(|o| o.as_str()).collect();
        self.log
            .lock()
            .unwrap()
            .push(format!("unlock {}", names.join(" ")));
        let prompt = if self.ask { "/prompt" } else { "/" };
        (vec![], prompt.try_into().unwrap())
    }
}

struct Session(Log);

#[zbus::interface(name = "org.freedesktop.Secret.Session")]
impl Session {
    fn close(&self) {
        self.0.lock().unwrap().push("close".into());
    }
}

/// An unlock dialog that stays open.
struct Prompt(Log);

#[zbus::interface(name = "org.freedesktop.Secret.Prompt")]
impl Prompt {
    fn prompt(&self, _window: String) {
        self.0.lock().unwrap().push("prompt".into());
    }
}

struct Collection;

#[zbus::interface(name = "org.freedesktop.Secret.Collection")]
impl Collection {
    fn search_items(
        &self,
        _attributes: std::collections::HashMap<String, String>,
    ) -> Vec<zbus::OwnedObjectPath> {
        vec![ENTRY.try_into().unwrap()]
    }
}

struct Item;

#[zbus::interface(name = "org.freedesktop.Secret.Item")]
impl Item {
    #[allow(clippy::type_complexity)]
    fn get_secret(
        &self,
        session: zbus::OwnedObjectPath,
    ) -> ((zbus::OwnedObjectPath, Vec<u8>, Vec<u8>, String),) {
        let secret = format!("https://clrd.example.com\n{TOKEN}").into_bytes();
        ((session, vec![], secret, "text/plain".into()),)
    }

    fn delete(&self) -> zbus::OwnedObjectPath {
        "/".try_into().unwrap()
    }
}

fn locked(bus: &Bus, ask: bool) -> (zbus::Connection, Log) {
    let log = Log::default();
    let service = zbus::block_on(
        zbus::connection::Builder::address(bus.1.as_str())
            .name("org.freedesktop.secrets")
            .serve_at(
                "/org/freedesktop/secrets",
                Locked {
                    log: log.clone(),
                    ask,
                },
            )
            .serve_at("/session", Session(log.clone()))
            .serve_at("/prompt", Prompt(log.clone()))
            .serve_at("/org/freedesktop/secrets/aliases/default", Collection)
            .serve_at(ENTRY, Item)
            .build(),
    )
    .unwrap();
    (service, log)
}

/// The entry is unlocked as well as the keyring, and each session that was
/// opened is closed again (#608).
#[test]
fn each_entry_is_unlocked_too_and_each_session_closed() {
    let bus = Bus::start();
    let (_service, log) = locked(&bus, false);
    let keys = Keychain::new(bus.connect());
    assert_eq!(keys.load().unwrap().map(|l| l.token), Some(TOKEN.into()));
    keys.clear().unwrap();
    let default = "unlock /org/freedesktop/secrets/aliases/default".to_string();
    let entry = format!("unlock {ENTRY}");
    let close = "close".to_string();
    let open = "open".to_string();
    assert_eq!(
        *log.lock().unwrap(),
        [open, default.clone(), entry.clone(), close, default, entry]
    );
}

/// Only the keyring can say its unlock dialog is done, and the wait for it
/// ends when the keyring goes away mid-dialog (#608).
#[test]
fn only_the_keyring_ends_an_unlock_and_its_going_away_does_too() {
    let bus = Bus::start();
    let (service, log) = locked(&bus, true);
    let keys = Keychain::new(bus.connect());
    let (tx, rx) = std::sync::mpsc::channel();
    std::thread::spawn(move || tx.send(keys.load()).unwrap());
    wait("the dialog is up", || {
        log.lock().unwrap().iter().any(|l| l == "prompt")
    });

    // Another program on the bus saying the dialog is done is not heard.
    let other = bus.connect();
    zbus::block_on(other.emit_signal(
        None::<&str>,
        "/prompt",
        "org.freedesktop.Secret.Prompt",
        "Completed",
        &(false, zbus::Variant::from("")),
    ))
    .unwrap();
    let wait_for = std::time::Duration::from_secs(1);
    assert!(rx.recv_timeout(wait_for).is_err(), "a stranger ended it");
    let unlocks = log
        .lock()
        .unwrap()
        .iter()
        .filter(|l| l.starts_with("unlock"))
        .count();
    assert_eq!(unlocks, 1, "a stranger ended the first dialog");

    zbus::block_on(service.release_name("org.freedesktop.secrets")).unwrap();
    let got = rx
        .recv_timeout(5 * wait_for)
        .expect("still waiting after the keyring went away");
    assert!(got.is_err());
}
