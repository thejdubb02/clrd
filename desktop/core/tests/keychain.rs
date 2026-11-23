//! The keychain against a real Secret Service (GNOME Keyring) on a private
//! bus, with a home folder of its own. Nothing here touches a real keyring.
#![cfg(target_os = "linux")]

mod common;

use clrd_core::keychain::Keychain;
use common::{state_path, wait, Bus};
use std::io::Write;
use std::path::Path;
use std::process::{Child, Command, Stdio};

const TOKEN: &str = "clrd_keychaintesttoken000000000000000";

struct Kill(Child);

impl Drop for Kill {
    fn drop(&mut self) {
        let _ = self.0.kill();
        let _ = self.0.wait();
    }
}

/// True when any file under `dir` holds `needle` as plain bytes.
fn on_disk(dir: &Path, needle: &[u8]) -> bool {
    std::fs::read_dir(dir).unwrap().any(|e| {
        let path = e.unwrap().path();
        if path.is_dir() {
            return on_disk(&path, needle);
        }
        // Sockets hold nothing; a file that cannot be read fails the test.
        path.is_file()
            && std::fs::read(&path)
                .unwrap()
                .windows(needle.len())
                .any(|w| w == needle)
    })
}

/// A GNOME Keyring on `bus` keeping its files under `home`, unlocked with
/// the password the login screen would hand it.
fn keyring(bus: &Bus, home: &Path) -> Kill {
    let mut daemon = Command::new("gnome-keyring-daemon")
        .args(["--foreground", "--unlock", "--components=secrets"])
        .env("DBUS_SESSION_BUS_ADDRESS", &bus.1)
        .env("HOME", home)
        .env("XDG_DATA_HOME", home.join("data"))
        .env("XDG_RUNTIME_DIR", home.join("run"))
        .stdin(Stdio::piped())
        .stdout(Stdio::null())
        .spawn()
        .expect("gnome-keyring is needed for this test");
    daemon
        .stdin
        .take()
        .unwrap()
        .write_all(b"test-password")
        .unwrap();
    Kill(daemon)
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
    keys.save("https://old.example.com", "clrd_old").unwrap();
    keys.save("https://clrd.example.com", TOKEN).unwrap();
    let linked = Some(("https://clrd.example.com".to_string(), TOKEN.to_string()));
    assert_eq!(keys.load(), Ok(linked.clone()));

    // After a reboot: a new keyring process and a new connection read the
    // one entry back, so it was kept and not only held in memory.
    drop(daemon);
    wait("keyring down", || keys.load().is_err());
    let _daemon = keyring(&bus, &home);
    let again = Keychain::new(bus.connect());
    wait("keyring back", || again.load().is_ok());
    assert_eq!(again.load(), Ok(linked));
    // What the keyring wrote does not hold the token in the clear. This
    // line checks the keyring, not this app, which writes no file at all.
    assert!(!on_disk(&home, TOKEN.as_bytes()));

    again.clear().unwrap();
    assert_eq!(again.load(), Ok(None));
}

#[test]
fn with_no_keychain_nothing_is_stored_and_it_says_so() {
    let bus = Bus::start();
    let keys = Keychain::new(bus.connect());
    assert!(keys.save("https://clrd.example.com", TOKEN).is_err());
    assert!(keys.load().is_err());
    assert!(keys.clear().is_err());
}
