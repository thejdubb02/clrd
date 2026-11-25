//! This computer as a linked device, against the fake server and a real
//! Secret Service (GNOME Keyring) on a private bus. Linking itself is proved
//! against a real server in `real_server.rs`.
#![cfg(target_os = "linux")]

mod common;

use clrd_core::device::Device;
use clrd_core::keychain::{Keychain, Linked};
use common::{keyring, linked, state_path, wait, Bus, Fake, TOKEN};

#[test]
fn one_window_session_that_ends_with_the_link() {
    let fake = Fake::start();
    let (bus, _daemon, keys, _home) = linked(&fake);
    let device = Device::new(Ok(Keychain::new(bus.connect())), "test desktop");
    assert_eq!(device.view().server, fake.base);

    // Each start of the app replaces the window's session: the keychain
    // keeps the last one so the server can be told to end it.
    let first = device.session().unwrap();
    assert_eq!(keys.load().unwrap().unwrap().session, first);
    let second = device.session().unwrap();
    assert_ne!(first, second);
    assert_eq!(
        fake.sessions(),
        [second.split(';').next().unwrap().to_string()]
    );
    // As after a restart: a new app reads the session back and replaces it.
    let again = Device::new(Ok(Keychain::new(bus.connect())), "test desktop");
    let third = again.session().unwrap();
    assert_eq!(fake.sessions().len(), 1);
    assert_eq!(keys.load().unwrap().unwrap().session, third);

    again.unlink();
    assert!(fake.unlinked());
    assert!(fake.sessions().is_empty());
    assert_eq!(keys.load(), Ok(None));
    assert_eq!(again.linked(), None);
    assert_eq!(again.view().error, "");
    // The token never went into an address.
    assert!(fake.requests().iter().all(|r| !r.contains(TOKEN)));
}

/// A server that will not let this device remove itself (#609) still loses
/// the window session, and this computer still forgets the token.
#[test]
fn a_refused_unlink_still_ends_the_session_and_forgets_the_token() {
    let fake = Fake::start();
    let (bus, _daemon, keys, _home) = linked(&fake);
    let device = Device::new(Ok(Keychain::new(bus.connect())), "test desktop");
    device.session().unwrap();
    assert_eq!(fake.sessions().len(), 1);

    fake.refuse_unlink(true);
    device.unlink();
    assert!(fake.sessions().is_empty(), "the window session is ended");
    assert_eq!(keys.load(), Ok(None), "the keychain entry is gone");
    assert_eq!(device.linked(), None);
    assert!(!fake.unlinked());
    assert!(device.view().error.contains("may still list this device"));
    assert!(device.session().is_err());
}

/// A session asked for just before an unlink does not outlive it (#636),
/// even on a server that refuses the device's removal.
#[test]
fn a_session_made_while_unlinking_does_not_outlive_the_unlink() {
    let fake = Fake::start();
    let (bus, _daemon, keys, _home) = linked(&fake);
    let device = Device::new(Ok(Keychain::new(bus.connect())), "test desktop");
    fake.refuse_unlink(true);
    fake.slow_session();

    std::thread::scope(|s| {
        s.spawn(|| device.session());
        wait("the session is asked for", || {
            fake.requests()
                .iter()
                .any(|r| r.starts_with("POST /api/v1/session "))
        });
        device.unlink();
    });
    assert_eq!(fake.sessions(), [""; 0], "no window session is left");
    assert_eq!(keys.load(), Ok(None));
    assert_eq!(device.linked(), None);
}

#[test]
fn a_refused_token_is_said_and_kept() {
    let fake = Fake::start();
    let home = state_path().with_file_name("home");
    std::fs::create_dir_all(home.join("run")).unwrap();
    let bus = Bus::start();
    let _daemon = keyring(&bus, &home);
    let keys = Keychain::new(bus.connect());
    wait("keyring up", || keys.load().is_ok());
    let stale = Linked {
        base: fake.base.clone(),
        token: "clrd_revoked".into(),
        session: String::new(),
    };
    keys.save(&stale).unwrap();

    let device = Device::new(Ok(Keychain::new(bus.connect())), "test desktop");
    assert!(device.session().is_err());
    assert!(device.view().refused);
    // Only the user unlinks: a server being set up again answers the same.
    assert_eq!(keys.load(), Ok(Some(stale)));
}

/// A link the browser handed over (#626) only fills the box. Any page can
/// make the browser do that, so nothing goes to the server it names.
#[test]
fn a_link_from_outside_is_offered_once_and_asks_the_server_nothing() {
    let fake = Fake::start();
    let link = format!(
        "clrd://link?server={}&code=abc123",
        fake.base.replace(':', "%3A").replace('/', "%2F")
    );
    let device = Device::new(Err("no keychain here".into()), "test desktop");

    device.offer(&link);
    assert_eq!(device.view().error, "no keychain here", "still said");
    assert_eq!(device.take_offer(), Some((link.clone(), fake.base.clone())));
    assert_eq!(device.take_offer(), None, "offered once");

    // Not a link, a server name dressed up as another, or far too long to
    // be one: said, and not offered.
    for bad in [
        "clrd://pair?server=https%3A%2F%2Fclrd.example.com&code=abc123".to_string(),
        "clrd://link?server=https%3A%2F%2Fclrd.example.com%40evil.example&code=abc123".to_string(),
        format!("{link}&{}", "x".repeat(2048)),
    ] {
        device.offer(&link);
        device.offer(&bad);
        assert!(device.view().error.contains("not a clrd device link"));
        assert_eq!(device.take_offer(), None);
    }
    assert_eq!(fake.requests(), Vec::<String>::new());

    // Linked while an offer waited for the screen: the offer is gone.
    let (bus, _daemon, keys, _home) = linked(&fake);
    let saved = keys.load().unwrap().unwrap();
    keys.clear().unwrap();
    let device = Device::new(Ok(Keychain::new(bus.connect())), "test desktop");
    device.offer(&link);
    keys.save(&saved).unwrap();
    device.reload();
    assert_eq!(device.take_offer(), None);

    // Linked already: the link in use stays, and the offer is dropped.
    device.offer(&link);
    assert!(device.view().error.contains("already linked"));
    assert_eq!(device.take_offer(), None);
    assert_eq!(device.view().server, fake.base);
    assert_eq!(fake.requests(), Vec::<String>::new());
}

#[test]
fn nothing_is_asked_for_without_a_keychain_or_over_plain_http_unasked() {
    let fake = Fake::start();
    let link = format!("{}/link?c=abc123", fake.base);

    // No keyring on this bus, so no token is asked for.
    let bus = Bus::start();
    let device = Device::new(Ok(Keychain::new(bus.connect())), "test desktop");
    assert!(!device.view().error.is_empty());
    assert!(device.start(&link, true, "").is_err());
    // No bus at all.
    let device = Device::new(Err("no keychain here".into()), "test desktop");
    assert_eq!(device.view().error, "no keychain here");
    assert_eq!(
        device.start(&link, true, ""),
        Err("no keychain here".into())
    );

    // Plain http to another computer waits for a yes; not a link is an error.
    assert_eq!(
        device.start("http://192.168.1.5:8080/link?c=abc123", false, ""),
        Ok(false)
    );
    assert_eq!(device.view().error, "");
    assert!(device
        .start("https://clrd.example.com/pair?c=abc123", true, "")
        .is_err());

    assert_eq!(fake.requests(), Vec::<String>::new());
}
