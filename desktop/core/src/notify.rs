//! Desktop notifications (`docs/SPEC.md` section 8.1). The rules live here,
//! once; `os` is the part that talks to the system: the notification service
//! on the session bus on Linux, toasts on Windows.
//!
//! The rules:
//!
//! - A clear from another device always closes the notification here.
//! - The Clear button clears everywhere (`via: desktop_button`).
//! - Closing a notification only hides it on this computer and posts
//!   nothing, unless the user turned on "closing clears everywhere"
//!   (`via: desktop_close`). Only a close by the user counts: one that
//!   expired, or that we closed ourselves, never posts.
//!
//! Titles and messages come from whoever can publish to the server, so they
//! are treated as untrusted text: escaped where the system reads markup, and
//! never used as anything but text.

use crate::client::Server;
use crate::engine::{Notification, Sink};
use std::collections::{HashMap, HashSet};
use std::sync::atomic::AtomicBool;
use std::sync::{mpsc, Arc, Mutex};

#[cfg(target_os = "linux")]
#[path = "notify_linux.rs"]
mod os;
#[cfg(windows)]
#[path = "notify_windows.rs"]
mod os;

use os::Os;

#[derive(Default)]
struct Shown {
    /// What is on screen: clrd id to the system's id and the notification.
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
    os: Os,
    server: Arc<Server>,
    on_open: Box<dyn Fn(&Notification) + Send + Sync>,
    shown: Mutex<Shown>,
}

/// Shows the engine's notifications on the desktop.
pub struct Notifier(Arc<Inner>);

/// What the user did to a notification, as the system reported it.
enum Signal {
    /// The system's id, and whether this close clears everywhere: the user
    /// closed it and the "closing clears everywhere" setting was on when the
    /// close arrived.
    Closed(u32, bool),
    /// The system's id and the button: `clear`, or anything else for a click.
    Action(u32, String),
}

impl Notifier {
    /// `conn` is the session bus; the notifier owns it and closes it when
    /// dropped. Build it with a method timeout, or a notification service
    /// that stops answering holds the sync thread. `close_clears` is the
    /// "closing clears everywhere" setting, read as each close arrives.
    /// `on_open` runs when a notification is clicked.
    #[cfg(target_os = "linux")]
    pub fn new(
        conn: zbus::Connection,
        server: Arc<Server>,
        close_clears: Arc<AtomicBool>,
        on_open: impl Fn(&Notification) + Send + Sync + 'static,
    ) -> zbus::Result<Self> {
        let (tx, rx) = mpsc::channel();
        Ok(Self::with(
            Os::new(conn, close_clears, tx)?,
            rx,
            server,
            on_open,
        ))
    }

    /// `app_id` is the name Windows knows the installed app by: the one on
    /// its Start menu shortcut. `close_clears` is the "closing clears
    /// everywhere" setting, read as each close arrives. `on_open` runs when
    /// a notification is clicked. Never fails: where Windows will not show
    /// a notification, each one is logged and skipped.
    #[cfg(windows)]
    pub fn new(
        app_id: &str,
        server: Arc<Server>,
        close_clears: Arc<AtomicBool>,
        on_open: impl Fn(&Notification) + Send + Sync + 'static,
    ) -> Self {
        let (tx, rx) = mpsc::channel();
        Self::with(Os::new(app_id, close_clears, tx), rx, server, on_open)
    }

    fn with(
        os: Os,
        rx: mpsc::Receiver<Signal>,
        server: Arc<Server>,
        on_open: impl Fn(&Notification) + Send + Sync + 'static,
    ) -> Self {
        let inner = Arc::new(Inner {
            os,
            server,
            on_open: Box::new(on_open),
            shown: Mutex::default(),
        });
        let weak = Arc::downgrade(&inner);
        // One thread handles the signals in the order they came. It ends
        // when the notifier is gone or the system side stops.
        // ponytail: a slow server holds up the next click for as long as the
        // dismiss call takes (30 s at most). More workers if that shows.
        std::thread::spawn(move || {
            for signal in rx {
                let Some(inner) = weak.upgrade() else { return };
                match signal {
                    Signal::Closed(id, clears) => inner.closed(id, clears),
                    Signal::Action(id, key) => inner.action(id, &key),
                }
            }
        });
        Self(inner)
    }
}

/// Text as markup shows it: nothing in it is read as a tag.
fn escape(text: &str) -> String {
    text.replace('&', "&amp;")
        .replace('<', "&lt;")
        .replace('>', "&gt;")
}

/// The title line: the title, or the topic when there is none.
fn summary(n: &Notification) -> &str {
    if n.title.is_empty() {
        &n.topic
    } else {
        &n.title
    }
}

/// A Windows toast, as the XML Windows takes. A click opens the app's
/// window and nothing else: the sender's `click` address is never put in,
/// so a toast cannot start a program or open a page by itself.
#[cfg(any(windows, test))]
fn toast_xml(n: &Notification, alert: bool) -> String {
    // Windows refuses a toast over about 5 KB, and one with a character XML
    // does not allow. It shows a few lines at most, so the cut loses nothing
    // the user would have seen.
    let text = |s: &str, most: usize| {
        let kept: String = s
            .chars()
            .filter(|c| matches!(c, '\t' | '\n' | ' '..='\u{d7ff}' | '\u{e000}'..='\u{fffd}'))
            .take(most)
            .collect();
        escape(&kept)
    };
    // clrd priority is 1 to 5; 1 and 2 are the quiet ones.
    let quiet = !alert || n.silenced || matches!(n.priority, 1 | 2);
    // ponytail: priority 5 looks like any other; scenario="urgent" would let
    // it through Do not disturb, on Windows 11 only.
    format!(
        "<toast launch=\"open\"><visual><binding template=\"ToastGeneric\">\
         <text>{}</text><text>{}</text></binding></visual>\
         <actions><action content=\"Clear\" arguments=\"clear\"/></actions>\
         <audio silent=\"{quiet}\"/></toast>",
        text(summary(n), 200),
        text(&n.message, 1000),
    )
}

impl Inner {
    fn put(&self, n: &Notification, alert: bool) {
        // Held across the call, so a signal about the new id waits until the
        // id is in the map.
        let mut shown = self.shown.lock().unwrap();
        if !alert && shown.hidden.contains(&n.id) {
            return;
        }
        shown.hidden.remove(&n.id);
        let replaces = shown.open.get(&n.id).map_or(0, |e| e.0);
        match self.os.notify(n, alert, replaces) {
            Ok(os_id) => {
                // A restarted service starts its ids over: whatever we
                // still hold under this id is gone from the screen.
                shown.open.retain(|_, e| e.0 != os_id);
                shown.open.insert(n.id.clone(), (os_id, n.clone()));
            }
            Err(e) => log::warn!("notification not shown: {e}"),
        }
    }

    /// Forgets the notification the system knows as `os_id`, if it is ours.
    /// Taken out before anything is closed, so the close the system reports
    /// after our own finds nothing, whatever reason it carries.
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

    fn closed(&self, os_id: u32, clears: bool) {
        let Some(n) = self.take(os_id) else { return };
        if clears {
            self.clear(&n, "desktop_close");
        } else {
            self.hide(n);
        }
    }

    fn action(&self, os_id: u32, key: &str) {
        let Some(n) = self.take(os_id) else { return };
        // Not every system takes a notification down when a button is used.
        self.os.close(os_id);
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
            self.0.os.close(os_id);
        }
    }
}

impl Drop for Notifier {
    /// Takes down what is showing, so the next start does not show it twice.
    // ponytail: a crash skips this and leaves them up until the next start
    // replaces nothing and shows them again. Saving the system's ids would
    // fix it, if it is ever seen.
    fn drop(&mut self) {
        let open = std::mem::take(&mut self.0.shown.lock().unwrap().open);
        for (os_id, _) in open.into_values() {
            self.0.os.close(os_id);
        }
        self.0.os.stop();
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn n(title: &str, message: &str, priority: u8, silenced: bool) -> Notification {
        Notification {
            id: "01JD0000000000000000000000".into(),
            topic: "backups".into(),
            title: title.into(),
            message: message.into(),
            priority,
            state: "active".into(),
            silenced,
            click: "https://evil.example/run.exe".into(),
            seq: 1,
        }
    }

    #[test]
    fn a_toast_carries_only_text_from_the_sender() {
        let xml = toast_xml(
            &n(
                "a<b>&\u{0}\u{b}",
                "</text><action arguments=\"x\"/>",
                3,
                false,
            ),
            true,
        );
        assert_eq!(
            xml,
            "<toast launch=\"open\"><visual><binding template=\"ToastGeneric\">\
             <text>a&lt;b&gt;&amp;</text>\
             <text>&lt;/text&gt;&lt;action arguments=\"x\"/&gt;</text></binding></visual>\
             <actions><action content=\"Clear\" arguments=\"clear\"/></actions>\
             <audio silent=\"false\"/></toast>"
        );
        // No title: the topic stands in. The click address is nowhere.
        let xml = toast_xml(&n("", "hi", 3, false), true);
        assert!(xml.contains("<text>backups</text><text>hi</text>"));
        assert!(!xml.contains("evil"));
    }

    #[test]
    fn a_toast_is_cut_to_what_windows_takes() {
        let xml = toast_xml(&n(&"é".repeat(5000), &"&".repeat(5000), 3, false), true);
        assert_eq!(xml.matches('é').count(), 200);
        assert_eq!(xml.matches("&amp;").count(), 1000);
    }

    #[test]
    fn a_toast_makes_a_sound_only_for_a_new_loud_one() {
        let silent = |n: &Notification, alert| toast_xml(n, alert).contains("silent=\"true\"");
        assert!(!silent(&n("", "", 3, false), true));
        assert!(!silent(&n("", "", 0, false), true));
        assert!(silent(&n("", "", 3, false), false));
        assert!(silent(&n("", "", 3, true), true));
        assert!(silent(&n("", "", 2, false), true));
    }
}
