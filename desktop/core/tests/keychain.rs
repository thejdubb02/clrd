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
