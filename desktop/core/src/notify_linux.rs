//! The Linux side of `notify`: `org.freedesktop.Notifications` on the
//! session bus.

use super::{escape, summary, Signal};
use crate::engine::Notification;
use std::collections::HashMap;
use std::pin::Pin;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{mpsc, Arc};
use zbus::export::futures_core::Stream;
use zbus::{block_on, Connection, MessageStream, Variant};

const NAME: &str = "org.freedesktop.Notifications";
const PATH: &str = "/org/freedesktop/Notifications";
/// `NotificationClosed` reason 2: the user dismissed it.
const BY_USER: u32 = 2;

pub struct Os {
    conn: Connection,
}

impl Os {
    pub fn new(
        conn: Connection,
        close_clears: Arc<AtomicBool>,
        tx: mpsc::Sender<Signal>,
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
        std::thread::spawn(move || listen(stream, close_clears, tx));
        Ok(Self { conn })
    }

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

    /// Shows `n`, in place of the one the service knows as `replaces` (0 for
    /// none), and gives the service's id for it.
    pub fn notify(&self, n: &Notification, alert: bool, replaces: u32) -> zbus::Result<u32> {
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
        let body = if self.markup() {
            escape(&n.message)
        } else {
            n.message.clone()
        };
        let actions = ["default", "Open", "clear", "Clear"];
        // -1: the service decides how long it stays up.
        self.call(
            "Notify",
            &(
                "clrd",
                replaces,
                "",
                summary(n),
                &body,
                &actions[..],
                hints,
                -1i32,
            ),
        )
        .and_then(|reply| reply.body().deserialize::<u32>())
    }

    pub fn close(&self, os_id: u32) {
        let _ = self.call("CloseNotification", &os_id);
    }

    /// Ends the signal thread.
    pub fn stop(&self) {
        let _ = block_on(self.conn.clone().close());
    }
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
            Some("NotificationClosed") => body.deserialize::<(u32, u32)>().ok().map(|(id, why)| {
                Signal::Closed(id, why == BY_USER && close_clears.load(Ordering::Relaxed))
            }),
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
