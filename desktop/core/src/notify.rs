//! Desktop notifications on Linux, through `org.freedesktop.Notifications`
//! on the session bus (`docs/SPEC.md` section 8.1).
//!
//! The rules, in one place:
//!
//! - A clear from another device always closes the notification here.
//! - The Clear button clears everywhere (`via: desktop_button`).
//! - Closing a notification only hides it on this computer and posts
//!   nothing, unless the user turned on "closing clears everywhere"
//!   (`via: desktop_close`). Only a close by the user counts: one that
//!   expired, or that we closed ourselves, never posts.
//!
//! Titles and messages come from whoever can publish to the server, so they
//! are treated as untrusted text: escaped where the notification service
//! reads markup, and never used as anything but text.

use crate::client::Server;
use crate::engine::{Notification, Sink};
use std::collections::{HashMap, HashSet};
use std::pin::Pin;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{mpsc, Arc, Mutex};
use zbus::export::futures_core::Stream;
use zbus::{block_on, Connection, MessageStream, Variant};

const NAME: &str = "org.freedesktop.Notifications";
const PATH: &str = "/org/freedesktop/Notifications";
/// `NotificationClosed` reason 2: the user dismissed it.
const BY_USER: u32 = 2;

#[derive(Default)]
struct Shown {
    /// What is on screen: clrd id to the service's id and the notification.
    open: HashMap<String, (u32, Notification)>,
    /// What the user closed or clicked here while it is still open on the
    /// server. A quiet update does not bring these back; an alert does.
    /// Not saved: after a restart the open ones come back, without a sound.
    hidden: HashSet<String>,
    /// Clears on their way to the server. One that fails brings its
    /// notification back, unless another device cleared it in the meantime.
    clearing: HashSet<String>,
}

struct Inner {
    conn: Connection,
    server: Arc<Server>,
    on_open: Box<dyn Fn(&Notification) + Send + Sync>,
    shown: Mutex<Shown>,
}

/// Shows the engine's notifications on the Linux desktop.
pub struct Notifier(Arc<Inner>);

impl Notifier {
    /// `conn` is the session bus; the notifier owns it and closes it when
    /// dropped. Build it with a method timeout, or a notification service
    /// that stops answering holds the sync thread. `close_clears` is the
    /// "closing clears everywhere" setting, read as each close arrives.
    /// `on_open` runs when a notification is clicked.
    pub fn new(
        conn: Connection,
        server: Arc<Server>,
        close_clears: Arc<AtomicBool>,
        on_open: impl Fn(&Notification) + Send + Sync + 'static,
    ) -> zbus::Result<Self> {
        let stream = block_on(async {
            // ponytail: any program on the user's own session bus can send
            // these signals; the bus itself is the trust boundary. Match on
            // the service's owner if that ever stops being enough.
            let rule = zbus::MatchRule::builder()
                .msg_type(zbus::message::Type::Signal)
                .interface(NAME)
                .path(PATH)
                .build()?;
            // A full queue here would stall the connection. The listener
            // only hands signals on, so it never falls far behind; the long
            // queue is spare room.
            MessageStream::for_match_rule(rule, &conn, std::num::NonZero::new(4096)).await
        })?;
        let inner = Arc::new(Inner {
            conn,
            server,
            on_open: Box::new(on_open),
            shown: Mutex::default(),
        });
        let weak = Arc::downgrade(&inner);
        let (tx, rx) = mpsc::channel();
        std::thread::spawn(move || listen(stream, close_clears, tx));
        // One thread handles the signals in the order they came. It ends
        // when the notifier is gone or the listener stops.
        // ponytail: a slow server holds up the next click for as long as the
        // dismiss call takes (30 s at most). More workers if that shows.
        std::thread::spawn(move || {
            for signal in rx {
                let Some(inner) = weak.upgrade() else { return };
                match signal {
                    Signal::Closed(id, reason, clears) => inner.closed(id, reason, clears),
                    Signal::Action(id, key) => inner.action(id, &key),
                }
            }
        });
        Ok(Self(inner))
    }
}

enum Signal {
    /// The service's id, the reason, and what the "closing clears
    /// everywhere" setting said when the close arrived.
    Closed(u32, u32, bool),
    Action(u32, String),
}

/// Reads the service's signals until the bus connection ends, and hands them
/// on at once: a close is judged by the setting as it was when the user
/// closed it, however long the ones before it take to handle (#685).
fn listen(mut stream: MessageStream, close_clears: Arc<AtomicBool>, tx: mpsc::Sender<Signal>) {
    while let Some(Ok(msg)) = block_on(std::future::poll_fn(|cx| {
        Pin::new(&mut stream).poll_next(cx)
    })) {
        let header = msg.header();
        let body = msg.body();
        let signal = match header.member().map(|m| m.as_str()) {
            Some("NotificationClosed") => body
                .deserialize::<(u32, u32)>()
                .ok()
                .map(|(id, why)| Signal::Closed(id, why, close_clears.load(Ordering::Relaxed))),
            Some("ActionInvoked") => body
                .deserialize::<(u32, String)>()
                .ok()
                .map(|(id, key)| Signal::Action(id, key)),
            _ => None,
        };
        if signal.is_some_and(|s| tx.send(s).is_err()) {
            return;
        }
    }
}

fn escape(text: &str) -> String {
    text.replace('&', "&amp;")
        .replace('<', "&lt;")
        .replace('>', "&gt;")
}

impl Inner {
    fn call<B>(&self, method: &str, body: &B) -> zbus::Result<zbus::Message>
    where
        B: serde::Serialize + zbus::DynamicType,
    {
        // ponytail: no timeout of our own; a notification service that hangs
        // holds the caller for the bus's own reply timeout.
        block_on(
            self.conn
                .call_method(Some(NAME), PATH, Some(NAME), method, body),
        )
    }

    /// Whether the service reads the body as markup. Asked every time, since
    /// the service can be swapped for another while the app runs. No answer
    /// counts as markup: escaping text that did not need it only shows an
    /// `&amp;`, the other mistake lets a sender put links and images in the
    /// body.
    fn markup(&self) -> bool {
        self.call("GetCapabilities", &())
            .and_then(|reply| reply.body().deserialize::<Vec<String>>())
            .map_or(true, |caps| caps.iter().any(|c| c == "body-markup"))
    }

    fn put(&self, n: &Notification, alert: bool) {
        // Held across the call, so a signal about the new id waits until the
        // id is in the map.
        let mut shown = self.shown.lock().unwrap();
        if !alert && shown.hidden.contains(&n.id) {
            return;
        }
        shown.hidden.remove(&n.id);
        let replaces = shown.open.get(&n.id).map_or(0, |e| e.0);
        // clrd priority is 1 to 5, 5 the most urgent; 0 means not set.
        let urgency: u8 = match n.priority {
            _ if n.silenced => 0,
            1 | 2 => 0,
            5.. => 2,
            _ => 1,
        };
        let mut hints: HashMap<&str, Variant<'_>> = HashMap::new();
        hints.insert("urgency", urgency.into());
        hints.insert("suppress-sound", (!alert).into());
        let summary = if n.title.is_empty() {
            &n.topic
        } else {
            &n.title
        };
        let body = if self.markup() {
            escape(&n.message)
        } else {
            n.message.clone()
        };
        let actions = ["default", "Open", "clear", "Clear"];
        // -1: the service decides how long it stays up.
        let sent = self
            .call(
                "Notify",
                &(
                    "clrd",
                    replaces,
                    "",
                    summary,
                    &body,
                    &actions[..],
                    hints,
                    -1i32,
                ),
            )
            .and_then(|reply| reply.body().deserialize::<u32>());
        match sent {
            Ok(os_id) => {
                // A restarted service starts its ids over: whatever we
                // still hold under this id is gone from the screen.
                shown.open.retain(|_, e| e.0 != os_id);
                shown.open.insert(n.id.clone(), (os_id, n.clone()));
            }
            Err(e) => log::warn!("notification not shown: {e}"),
        }
    }

    /// Forgets the notification the service knows as `os_id`, if it is ours.
    /// Taken out before anything is closed, so the `NotificationClosed` that
    /// follows our own close finds nothing, whatever reason it carries.
    fn take(&self, os_id: u32) -> Option<Notification> {
        let mut shown = self.shown.lock().unwrap();
        let id = shown
            .open
            .iter()
            .find(|(_, e)| e.0 == os_id)
            .map(|(id, _)| id.clone())?;
        shown.open.remove(&id).map(|e| e.1)
    }

    fn hide(&self, n: Notification) {
        self.shown.lock().unwrap().hidden.insert(n.id);
    }

    /// Clears everywhere. If the server cannot be reached the notification
    /// comes back, without a sound, rather than vanish while still open.
    fn clear(&self, n: &Notification, via: &str) {
        self.shown.lock().unwrap().clearing.insert(n.id.clone());
        let sent = self.server.dismiss(&n.id, via);
        let still_ours = self.shown.lock().unwrap().clearing.remove(&n.id);
        if let Err(e) = sent {
            log::warn!("clear did not reach the server: {e}");
            if still_ours {
                self.put(n, false);
            }
        }
    }

    fn closed(&self, os_id: u32, reason: u32, clears: bool) {
        let Some(n) = self.take(os_id) else { return };
        if reason == BY_USER && clears {
            self.clear(&n, "desktop_close");
        } else {
            self.hide(n);
        }
    }

    fn action(&self, os_id: u32, key: &str) {
        let Some(n) = self.take(os_id) else { return };
        // Not every service takes a notification down when a button is used.
        let _ = self.call("CloseNotification", &os_id);
        if key == "clear" {
            self.clear(&n, "desktop_button");
        } else {
            (self.on_open)(&n);
            self.hide(n);
        }
    }
}

impl Sink for Notifier {
    fn show(&mut self, n: &Notification, alert: bool) {
        self.0.put(n, alert);
    }

    fn close(&mut self, id: &str) {
        let os_id = {
            let mut shown = self.0.shown.lock().unwrap();
            shown.hidden.remove(id);
            shown.clearing.remove(id);
            shown.open.remove(id).map(|e| e.0)
        };
        if let Some(os_id) = os_id {
            let _ = self.0.call("CloseNotification", &os_id);
        }
    }
}

impl Drop for Notifier {
    /// Takes down what is showing, so the next start does not show it twice.
    // ponytail: a crash skips this and leaves them up until the next start
    // replaces nothing and shows them again. Saving the service's ids would
    // fix it, if it is ever seen.
    fn drop(&mut self) {
        let open = std::mem::take(&mut self.0.shown.lock().unwrap().open);
        for (os_id, _) in open.into_values() {
            let _ = self.0.call("CloseNotification", &os_id);
        }
        // Ends the signal thread.
        let _ = block_on(self.0.conn.clone().close());
    }
}
