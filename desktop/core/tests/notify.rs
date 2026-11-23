//! The dismiss proof for Linux notifications: the notifier against a fake
//! `org.freedesktop.Notifications` on a private bus this file starts, and the
//! fake clrd server in `common`. Nothing here touches the real session bus.
#![cfg(target_os = "linux")]

mod common;

use clrd_core::notify::Notifier;
use clrd_core::{Engine, Notification, Server, Sink};
use common::{settle, state_path, wait, Fake, TOKEN};
use serde_json::json;
use std::collections::HashMap;
use std::io::{BufRead, BufReader};
use std::process::{Child, Command, Stdio};
use std::sync::atomic::{AtomicBool, AtomicU32, Ordering};
use std::sync::{Arc, Mutex};
use zbus::object_server::SignalEmitter;
use zbus::{block_on, Connection};

const NAME: &str = "org.freedesktop.Notifications";
const PATH: &str = "/org/freedesktop/Notifications";

/// A session bus of our own, gone when the test ends.
struct Bus(Child, String);

impl Bus {
    fn start() -> Bus {
        let mut child = Command::new("dbus-daemon")
            .args(["--session", "--nofork", "--print-address"])
            .stdout(Stdio::piped())
            .spawn()
            .expect("dbus-daemon is needed for these tests");
        let mut address = String::new();
        BufReader::new(child.stdout.take().unwrap())
            .read_line(&mut address)
            .unwrap();
        Bus(child, address.trim().to_string())
    }

    fn connect(&self) -> Connection {
        block_on(zbus::connection::Builder::address(self.1.as_str()).build()).unwrap()
    }
}

impl Drop for Bus {
    fn drop(&mut self) {
        let _ = self.0.kill();
        let _ = self.0.wait();
    }
}

/// The notification service, writing down what it was asked.
struct Service {
    calls: Arc<Mutex<Vec<String>>>,
    /// The last id handed out. Set back to 0 to act as a restarted service.
    next: Arc<AtomicU32>,
    markup: bool,
    /// The reason sent in the `NotificationClosed` that follows a
    /// `CloseNotification` call. Real services send 3.
    echo: Arc<AtomicU32>,
}

#[zbus::interface(name = "org.freedesktop.Notifications")]
impl Service {
    fn get_capabilities(&self) -> Vec<String> {
        let mut caps = vec!["actions".to_string(), "body".to_string()];
        if self.markup {
            caps.push("body-markup".to_string());
        }
        caps
    }

    #[allow(clippy::too_many_arguments)]
    fn notify(
        &mut self,
        app_name: String,
        replaces_id: u32,
        _app_icon: String,
        summary: String,
        body: String,
        actions: Vec<String>,
        hints: HashMap<String, zbus::OwnedVariant>,
        expire_timeout: i32,
    ) -> u32 {
        let id = if replaces_id == 0 {
            self.next.fetch_add(1, Ordering::Relaxed) + 1
        } else {
            replaces_id
        };
        assert_eq!(app_name, "clrd");
        assert_eq!(actions, ["default", "Open", "clear", "Clear"]);
        assert_eq!(expire_timeout, -1);
        let urgency: u8 = hints["urgency"].downcast_ref().unwrap();
        let quiet: bool = hints["suppress-sound"].downcast_ref().unwrap();
        self.calls.lock().unwrap().push(format!(
            "notify {id} replaces={replaces_id} urgency={urgency} quiet={quiet} [{summary}] [{body}]"
        ));
        id
    }

    async fn close_notification(
        &self,
        #[zbus(signal_emitter)] emitter: SignalEmitter<'_>,
        id: u32,
    ) {
        self.calls.lock().unwrap().push(format!("close {id}"));
        let reason = self.echo.load(Ordering::Relaxed);
        Self::notification_closed(&emitter, id, reason)
            .await
            .unwrap();
    }

    #[zbus(signal)]
    async fn notification_closed(
        emitter: &SignalEmitter<'_>,
        id: u32,
        reason: u32,
    ) -> zbus::Result<()>;
}

/// One desktop: a bus, the fake service, the fake clrd server and a notifier.
struct Desk {
    // Dropped in this order: the notifier first, the bus last.
    notifier: Option<Notifier>,
    service: Connection,
    calls: Arc<Mutex<Vec<String>>>,
    echo: Arc<AtomicU32>,
    next: Arc<AtomicU32>,
    close_clears: Arc<AtomicBool>,
    opened: Arc<Mutex<Vec<String>>>,
    server: Fake,
    _bus: Bus,
}

impl Desk {
    fn start() -> Desk {
        Desk::with(true, true)
    }

    fn with(markup: bool, publish: bool) -> Desk {
        let bus = Bus::start();
        let calls = Arc::new(Mutex::new(Vec::new()));
        let echo = Arc::new(AtomicU32::new(3));
        let next = Arc::new(AtomicU32::new(0));
        let service = block_on(
            zbus::connection::Builder::address(bus.1.as_str())
                .name(NAME)
                .serve_at(
                    PATH,
                    Service {
                        calls: calls.clone(),
                        next: next.clone(),
                        markup,
                        echo: echo.clone(),
                    },
                )
                .build(),
        )
        .unwrap();
        let server = Fake::start();
        if publish {
            for id in ["a", "b"] {
                server.publish(id, json!({}));
            }
        }
        let close_clears = Arc::new(AtomicBool::new(false));
        let opened = Arc::new(Mutex::new(Vec::new()));
        let seen = opened.clone();
        let notifier = Notifier::new(
            bus.connect(),
            Arc::new(Server::new(&server.base, TOKEN)),
            close_clears.clone(),
            move |n| seen.lock().unwrap().push(n.id.clone()),
        )
        .unwrap();
        Desk {
            notifier: Some(notifier),
            service,
            calls,
            echo,
            next,
            close_clears,
            opened,
            server,
            _bus: bus,
        }
    }

    fn show(&mut self, n: &Notification, alert: bool) {
        self.notifier.as_mut().unwrap().show(n, alert);
    }

    fn calls(&self) -> Vec<String> {
        self.calls.lock().unwrap().clone()
    }

    /// What the service sends when the user closes notification `id`
    /// (reason 2), it expires (1) or a program closes it (3).
    fn user_closes(&self, id: u32, reason: u32) {
        self.signal("NotificationClosed", &(id, reason));
    }

    fn user_presses(&self, id: u32, key: &str) {
        self.signal("ActionInvoked", &(id, key));
    }

    fn signal<B: serde::Serialize + zbus::DynamicType>(&self, name: &str, body: &B) {
        block_on(
            self.service
                .emit_signal(None::<&str>, PATH, NAME, name, body),
        )
        .unwrap();
    }
}

fn note(id: &str) -> Notification {
    Notification {
        id: id.into(),
        topic: "backups".into(),
        title: format!("title {id}"),
        message: "done".into(),
        priority: 3,
        state: "active".into(),
        ..Default::default()
    }
}

fn first(id: &str) -> String {
    format!("notify 1 replaces=0 urgency=1 quiet=false [title {id}] [done]")
}

#[test]
fn shows_with_the_right_sound_urgency_and_text() {
    let mut d = Desk::start();
    d.show(&note("a"), true);
    assert_eq!(d.calls(), [first("a")]);

    // An update replaces the same notification, with no second sound.
    d.show(
        &Notification {
            priority: 5,
            ..note("a")
        },
        false,
    );
    assert_eq!(
        d.calls()[1],
        "notify 1 replaces=1 urgency=2 quiet=true [title a] [done]"
    );

    // Low priority, a silenced one, and no title at all.
    d.show(
        &Notification {
            priority: 2,
            ..note("b")
        },
        true,
    );
    d.show(
        &Notification {
            priority: 5,
            silenced: true,
            title: String::new(),
            ..note("c")
        },
        false,
    );
    assert_eq!(
        d.calls()[2..],
        [
            "notify 2 replaces=0 urgency=0 quiet=false [title b] [done]",
            "notify 3 replaces=0 urgency=0 quiet=true [backups] [done]"
        ]
    );
}

#[test]
fn markup_from_a_sender_is_escaped_where_the_service_reads_markup() {
    let text = r#"<a href="https://evil.example">R&D</a>"#;
    let sent = Notification {
        message: text.into(),
        ..note("a")
    };

    let mut d = Desk::start();
    d.show(&sent, true);
    assert_eq!(
        d.calls(),
        ["notify 1 replaces=0 urgency=1 quiet=false [title a] \
          [&lt;a href=\"https://evil.example\"&gt;R&amp;D&lt;/a&gt;]"]
    );

    // A service that shows the body as plain text gets it as it is.
    let mut plain = Desk::with(false, false);
    plain.show(&sent, true);
    assert_eq!(
        plain.calls(),
        [format!(
            "notify 1 replaces=0 urgency=1 quiet=false [title a] [{text}]"
        )]
    );
}

#[test]
fn a_clear_from_another_device_closes_it_and_posts_nothing() {
    let mut d = Desk::start();
    // The hardest case: closing clears everywhere is on, and the service
    // reports our own close as if the user had done it.
    d.close_clears.store(true, Ordering::Relaxed);
    d.echo.store(2, Ordering::Relaxed);
    d.show(&note("a"), true);
    d.notifier.as_mut().unwrap().close("a");
    assert_eq!(d.calls(), [first("a"), "close 1".to_string()]);
    settle();
    assert!(
        d.server.dismissed().is_empty(),
        "{:?}",
        d.server.dismissed()
    );
    assert_eq!(d.calls().len(), 2);
}

#[test]
fn the_clear_button_clears_everywhere_once() {
    let d = {
        let mut d = Desk::start();
        d.close_clears.store(true, Ordering::Relaxed);
        d.echo.store(2, Ordering::Relaxed);
        d.show(&note("a"), true);
        d
    };
    d.user_presses(1, "clear");
    wait("the clear reaches the server", || {
        !d.server.dismissed().is_empty()
    });
    // The service also reports the close, as the user's; that must not
    // count as a second clear.
    d.user_closes(1, 2);
    settle();
    assert_eq!(
        d.server.dismissed(),
        [("a".to_string(), "desktop_button".to_string())]
    );
    assert_eq!(d.calls(), [first("a"), "close 1".to_string()]);
}

#[test]
fn closing_it_only_hides_it_here() {
    let mut d = Desk::start();
    d.show(&note("a"), true);
    d.user_closes(1, 2);
    settle();
    assert!(
        d.server.dismissed().is_empty(),
        "{:?}",
        d.server.dismissed()
    );

    // Still open on the server: an edit does not bring it back, a repeat does.
    d.show(&note("a"), false);
    assert_eq!(d.calls(), [first("a")]);
    d.show(&note("a"), true);
    assert_eq!(
        d.calls()[1..],
        ["notify 2 replaces=0 urgency=1 quiet=false [title a] [done]"]
    );
}

#[test]
fn closing_it_clears_everywhere_when_the_setting_is_on() {
    let mut d = Desk::start();
    d.close_clears.store(true, Ordering::Relaxed);
    d.show(&note("a"), true);
    d.user_closes(1, 2);
    wait("the clear reaches the server", || {
        !d.server.dismissed().is_empty()
    });
    settle();
    assert_eq!(
        d.server.dismissed(),
        [("a".to_string(), "desktop_close".to_string())]
    );
}

#[test]
fn an_expired_or_program_closed_one_never_clears() {
    let mut d = Desk::start();
    d.close_clears.store(true, Ordering::Relaxed);
    d.show(&note("a"), true);
    d.show(&note("b"), true);
    d.user_closes(1, 1); // expired
    d.user_closes(2, 3); // closed by a program

    // Signals are handled in order, so once this clear has arrived the two
    // closes before it have been handled too.
    d.server.publish("c", json!({}));
    d.show(&note("c"), true);
    d.user_presses(3, "clear");
    wait("the clear reaches the server", || {
        !d.server.dismissed().is_empty()
    });
    settle();
    assert_eq!(
        d.server.dismissed(),
        [("c".to_string(), "desktop_button".to_string())]
    );
}

#[test]
fn a_restarted_service_reusing_an_id_does_not_clear_the_old_one() {
    let mut d = Desk::start();
    d.show(&note("a"), true);
    d.next.store(0, Ordering::Relaxed); // the service restarts, a is gone
    d.show(&note("b"), true);
    d.user_presses(1, "clear");
    d.user_presses(1, "clear");
    wait("the clear reaches the server", || {
        !d.server.dismissed().is_empty()
    });
    settle();
    assert_eq!(
        d.server.dismissed(),
        [("b".to_string(), "desktop_button".to_string())]
    );
}

#[test]
fn another_programs_notification_is_left_alone() {
    let mut d = Desk::start();
    d.close_clears.store(true, Ordering::Relaxed);
    d.show(&note("a"), true);
    d.user_closes(999, 2);
    d.user_presses(999, "clear");
    settle();
    assert!(
        d.server.dismissed().is_empty(),
        "{:?}",
        d.server.dismissed()
    );
    assert_eq!(d.calls(), [first("a")]);
}

#[test]
fn a_click_opens_the_app_and_clears_nothing() {
    let mut d = Desk::start();
    d.close_clears.store(true, Ordering::Relaxed);
    d.echo.store(2, Ordering::Relaxed);
    d.show(&note("a"), true);
    d.user_presses(1, "default");
    wait("the click is handled", || {
        !d.opened.lock().unwrap().is_empty()
    });
    settle();
    assert_eq!(*d.opened.lock().unwrap(), ["a"]);
    assert!(
        d.server.dismissed().is_empty(),
        "{:?}",
        d.server.dismissed()
    );
    assert_eq!(d.calls(), [first("a"), "close 1".to_string()]);
}

#[test]
fn a_clear_that_cannot_reach_the_server_brings_it_back() {
    let mut d = Desk::start();
    d.show(&note("a"), true);
    d.server.offline();
    d.user_presses(1, "clear");
    wait("it comes back", || d.calls().len() == 3);
    assert_eq!(
        d.calls()[1..],
        [
            "close 1",
            "notify 2 replaces=0 urgency=1 quiet=true [title a] [done]"
        ]
    );
    assert!(d.server.dismissed().is_empty());
}

#[test]
fn quitting_takes_down_what_is_showing() {
    let mut d = Desk::start();
    d.show(&note("a"), true);
    d.show(&note("b"), true);
    d.notifier = None;
    let mut closes = d.calls()[2..].to_vec();
    closes.sort();
    assert_eq!(closes, ["close 1", "close 2"]);
}

/// The whole path, as the app runs it: server, sync engine, notifier, bus.
#[test]
fn clears_sync_both_ways_through_the_engine() {
    let mut d = Desk::with(true, false);
    let server = Arc::new(Server::new(&d.server.base, TOKEN));
    let engine = Arc::new(Mutex::new(
        Engine::open(state_path(), d.notifier.take().unwrap()).unwrap(),
    ));
    let stop = Arc::new(AtomicBool::new(false));
    let (e, s) = (engine.clone(), stop.clone());
    let thread = std::thread::spawn(move || clrd_core::run(&server, &e, &s, |_| {}));

    d.server.publish("a", json!({}));
    wait("a shows", || d.calls().len() == 1);
    d.server.set_state("a", "dismissed"); // cleared on a phone
    wait("a closes", || d.calls().len() == 2);
    assert_eq!(d.calls()[1], "close 1");
    assert!(d.server.dismissed().is_empty());

    d.server.publish("b", json!({}));
    wait("b shows", || d.calls().len() == 3);
    d.user_presses(2, "clear");
    wait("the clear reaches the server", || {
        !d.server.dismissed().is_empty()
    });
    settle();
    assert_eq!(
        d.server.dismissed(),
        [("b".to_string(), "desktop_button".to_string())]
    );
    assert_eq!(d.calls().len(), 4, "{:?}", d.calls());

    stop.store(true, Ordering::Relaxed);
    thread.join().unwrap().unwrap();
    drop(engine);
    drop(d);
}
