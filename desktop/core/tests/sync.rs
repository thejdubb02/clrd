//! The sync proof for the desktop core, against the fake server in `common`:
//! live changes, offline catch-up, a pruned log, a new database, a restart
//! and a refused token.

mod common;

use clrd_core::{Engine, Error, Next, Server, Status};
use common::{settle, state_path, wait, Client, Fake, Recorder, TOKEN};
use serde_json::json;

fn has(log: &[String], line: &str) -> bool {
    log.iter().any(|l| l == line)
}

fn ws_requests(fake: &Fake) -> Vec<String> {
    fake.requests()
        .into_iter()
        .filter(|r| r.contains("/api/v1/ws"))
        .collect()
}

fn list_requests(fake: &Fake) -> usize {
    fake.requests()
        .iter()
        .filter(|r| r.starts_with("GET /api/v1/notifications?"))
        .count()
}

/// A client that has caught up with everything published so far.
fn connected(fake: &Fake, path: &std::path::Path) -> Client {
    let c = Client::start(&fake.base, path, TOKEN);
    wait("connected", || c.last_status() == Some(Status::Connected));
    c
}

#[test]
fn live_show_and_clear_from_another_device() {
    let fake = Fake::start();
    let c = connected(&fake, &state_path());
    fake.publish("a", json!({}));
    wait("a shows", || has(&c.log.log(), "show a alert"));
    fake.set_state("a", "dismissed");
    wait("a closes", || has(&c.log.log(), "close a"));
    assert_eq!(c.log.log(), ["show a alert", "close a"]);
}

#[test]
fn dismiss_here_reaches_the_server_and_comes_back() {
    let fake = Fake::start();
    let c = connected(&fake, &state_path());
    fake.publish("a", json!({}));
    wait("a shows", || has(&c.log.log(), "show a alert"));
    Server::new(&fake.base, TOKEN)
        .dismiss("a", "desktop_button")
        .unwrap();
    assert_eq!(
        fake.dismissed(),
        [("a".to_string(), "desktop_button".to_string())]
    );
    wait("a closes", || has(&c.log.log(), "close a"));
}

#[test]
fn offline_catch_up_replays_from_the_last_seq() {
    let fake = Fake::start();
    fake.publish("a", json!({}));
    let c = connected(&fake, &state_path());
    assert_eq!(c.log.log(), ["show a alert"]);

    fake.offline();
    wait("offline noticed", || {
        c.last_status() != Some(Status::Connected)
    });
    fake.publish("b", json!({}));
    fake.publish("c", json!({})); // made and cleared while offline
    fake.set_state("c", "dismissed");
    fake.set_state("a", "dismissed"); // cleared on another device
    fake.online();

    wait("caught up", || {
        has(&c.log.log(), "close a") && has(&c.log.log(), "show b alert")
    });
    wait("connected again", || {
        c.last_status() == Some(Status::Connected)
    });
    assert!(
        !c.log
            .log()
            .iter()
            .any(|l| l.ends_with(" c") || l.contains(" c ")),
        "c must never show: {:?}",
        c.log.log()
    );
    // Replay, not a refetch: the reconnect asked for what came after seq 1.
    assert!(
        ws_requests(&fake).iter().any(|r| r.contains("since=1 ")),
        "{:?}",
        ws_requests(&fake)
    );
    assert_eq!(list_requests(&fake), 0);
    assert_eq!(c.engine.lock().unwrap().seq(), fake.seq());
}

#[test]
fn pruned_log_falls_back_to_the_open_list() {
    let fake = Fake::start();
    fake.publish("a", json!({}));
    let c = connected(&fake, &state_path());

    fake.offline();
    wait("offline noticed", || {
        c.last_status() != Some(Status::Connected)
    });
    fake.publish("b", json!({}));
    fake.set_state("a", "dismissed");
    fake.prune();
    fake.online();

    wait("caught up", || {
        has(&c.log.log(), "close a") && has(&c.log.log(), "show b alert")
    });
    assert_eq!(list_requests(&fake), 1);
    wait("cursor is the list's", || {
        c.engine.lock().unwrap().seq() == fake.seq()
    });
}

#[test]
fn new_epoch_drops_what_the_old_server_showed() {
    let fake = Fake::start();
    fake.publish("a", json!({}));
    let c = connected(&fake, &state_path());
    assert_eq!(c.log.log(), ["show a alert"]);

    // Same seq as the client holds, so only the epoch gives it away.
    fake.new_database(fake.seq());
    wait("a closes", || has(&c.log.log(), "close a"));
    assert_eq!(list_requests(&fake), 1);
    wait("connected again", || {
        c.last_status() == Some(Status::Connected)
    });
    fake.publish("b", json!({}));
    wait("b shows", || has(&c.log.log(), "show b alert"));
}

#[test]
fn restart_restores_quietly_and_catches_up() {
    let fake = Fake::start();
    let path = state_path();
    fake.publish("a", json!({}));
    fake.publish("b", json!({}));
    let mut first = connected(&fake, &path);
    first.stop().unwrap();

    fake.set_state("a", "dismissed"); // cleared while the app was closed
    let second = connected(&fake, &path);
    wait("a closes", || has(&second.log.log(), "close a"));
    // What was open comes back without a second alert.
    assert_eq!(
        second.log.log(),
        ["show a quiet", "show b quiet", "close a"]
    );
    assert!(
        ws_requests(&fake).last().unwrap().contains("since=2 "),
        "{:?}",
        ws_requests(&fake)
    );
}

#[test]
fn refused_token_stops_for_good() {
    let fake = Fake::start();
    let mut c = Client::start(
        &fake.base,
        &state_path(),
        "clrd_wrongtoken000000000000000000000",
    );
    wait("auth failed", || {
        c.last_status() == Some(Status::AuthFailed)
    });
    settle();
    assert_eq!(
        ws_requests(&fake).len(),
        1,
        "a refused token must not be retried"
    );
    assert!(matches!(c.stop(), Err(Error::Auth)));
}

/// Keeps every log line at the most talkative level, as a debug build of the
/// app with logging turned all the way up would.
struct Lines(std::sync::Mutex<Vec<String>>);

impl log::Log for Lines {
    fn enabled(&self, _: &log::Metadata) -> bool {
        true
    }
    fn log(&self, r: &log::Record) {
        self.0.lock().unwrap().push(r.args().to_string());
    }
    fn flush(&self) {}
}

static LINES: Lines = Lines(std::sync::Mutex::new(Vec::new()));

#[test]
fn token_is_never_in_a_url_a_log_or_a_debug_print() {
    // The only test that sets a logger, so this cannot fail as "already set".
    log::set_logger(&LINES).unwrap();
    log::set_max_level(log::LevelFilter::Trace);
    let fake = Fake::start();
    fake.publish("a", json!({}));
    let c = connected(&fake, &state_path());
    let server = Server::new(&fake.base, TOKEN);
    server.list_open().unwrap();
    server.dismiss("a", "desktop_button").unwrap();
    wait("a closes", || has(&c.log.log(), "close a"));
    assert!(fake.requests().len() >= 3);
    for line in fake.requests() {
        assert!(
            !line.contains(TOKEN) && !line.contains("auth="),
            "token in a URL: {line}"
        );
    }
    assert!(!format!("{server:?}").contains(TOKEN));
    for line in LINES.0.lock().unwrap().iter() {
        assert!(
            !line.contains(TOKEN) && !line.contains("Bearer"),
            "token in a log line: {line}"
        );
    }
}

// The engine alone, fed frames by hand.

fn live_engine() -> (Engine<Recorder>, Recorder, std::path::PathBuf) {
    let (log, path) = (Recorder::default(), state_path());
    let mut e = Engine::open(path.clone(), log.clone()).unwrap();
    assert_eq!(
        e.handle(r#"{"type":"ready","seq":0,"epoch":"e1"}"#)
            .unwrap(),
        Next::Ready
    );
    (e, log, path)
}

fn frame(seq: u64, kind: &str, id: &str, extra: serde_json::Value) -> String {
    let mut n = json!({"id": id, "state": "active", "seq": seq});
    n.as_object_mut()
        .unwrap()
        .extend(extra.as_object().cloned().unwrap());
    json!({"seq": seq, "type": kind, "notification": n}).to_string()
}

#[test]
fn alert_rules() {
    let (mut e, log, _) = live_engine();
    e.handle(&frame(1, "created", "a", json!({}))).unwrap();
    e.handle(&frame(2, "updated", "a", json!({}))).unwrap(); // an edit is quiet
    e.handle(&frame(3, "repeat", "a", json!({}))).unwrap(); // a live repeat rings
    e.handle(&frame(4, "state", "a", json!({"state": "acknowledged"})))
        .unwrap(); // stays, quietly
    e.handle(&frame(5, "created", "s", json!({"silenced": true})))
        .unwrap(); // a silence matched
    e.handle(&frame(6, "state", "a", json!({"state": "snoozed"})))
        .unwrap();
    e.handle(&frame(7, "state", "a", json!({"state": "active"})))
        .unwrap(); // the snooze ended
    e.handle(&frame(
        8,
        "state",
        "s",
        json!({"state": "some_future_state"}),
    ))
    .unwrap();
    assert_eq!(
        log.log(),
        [
            "show a alert",
            "show a quiet",
            "show a alert",
            "show a quiet",
            "show s quiet",
            "close a",
            "show a alert",
            "close s"
        ]
    );
    assert_eq!(e.seq(), 8);
}

#[test]
fn a_repeat_missed_while_offline_does_not_ring() {
    let (mut e, log, _) = live_engine();
    e.handle(&frame(1, "created", "a", json!({}))).unwrap();
    e.connecting();
    e.handle(&frame(2, "repeat", "a", json!({}))).unwrap();
    assert_eq!(
        e.handle(r#"{"type":"ready","seq":2,"epoch":"e1"}"#)
            .unwrap(),
        Next::Ready
    );
    assert_eq!(log.log(), ["show a alert", "show a quiet"]);
}

#[test]
fn marks_and_unknown_types_show_nothing() {
    let (mut e, log, _) = live_engine();
    assert_eq!(
        e.handle(r#"{"type":"mark","seq":4,"mark_id":"x"}"#)
            .unwrap(),
        Next::Continue
    );
    assert_eq!(e.seq(), 4, "a mark moves the cursor");
    assert_eq!(
        e.handle(r#"{"type":"settings","settings":{}}"#).unwrap(),
        Next::Continue
    );
    assert_eq!(
        e.handle(r#"{"type":"from_the_future","seq":9}"#).unwrap(),
        Next::Continue
    );
    assert_eq!(e.seq(), 4);
    assert!(log.log().is_empty());
}

#[test]
fn a_bad_frame_is_an_error_and_moves_nothing() {
    let (mut e, log, _) = live_engine();
    assert!(e.handle("not json").is_err());
    assert!(e.handle(r#"{"type":"created","seq":3}"#).is_err());
    assert_eq!(e.seq(), 0);
    assert!(log.log().is_empty());
}

#[test]
fn a_replay_from_another_epoch_is_thrown_away() {
    let (mut e, log, _) = live_engine();
    e.connecting();
    e.handle(&frame(1, "created", "x", json!({}))).unwrap();
    assert_eq!(
        e.handle(r#"{"type":"ready","seq":1,"epoch":"e2"}"#)
            .unwrap(),
        Next::Reset
    );
    assert!(
        log.log().is_empty(),
        "nothing from the other database may show"
    );
    assert_eq!(e.seq(), 0);
}

#[cfg(unix)]
#[test]
fn state_file_is_private_and_survives_reopen() {
    use std::os::unix::fs::PermissionsExt;
    let (mut e, _, path) = live_engine();
    e.handle(&frame(1, "created", "a", json!({}))).unwrap();
    assert_eq!(
        std::fs::metadata(&path).unwrap().permissions().mode() & 0o777,
        0o600
    );
    let log = Recorder::default();
    let again = Engine::open(path, log.clone()).unwrap();
    assert_eq!(again.seq(), 1);
    assert_eq!(log.log(), ["show a quiet"]);
}
