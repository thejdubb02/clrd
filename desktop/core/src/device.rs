//! This computer as a linked device: linking, the keychain entry, the
//! window's session and unlinking, as one state the app's screens show.
//!
//! No window code here. The app calls these from its own commands and puts
//! [`View`] on the screen.

use crate::client::{Error, Server};
use crate::keychain::{Keychain, Linked};
use crate::link::{self, Claim, Link};
use std::sync::Mutex;
use std::time::{Duration, Instant};

/// How often a waiting link asks the server whether it was approved.
const POLL: Duration = Duration::from_secs(2);

struct Pending {
    link: Link,
    base: String,
    asked: Option<Instant>,
}

#[derive(Default)]
struct Inner {
    linked: Option<Linked>,
    pending: Option<Pending>,
    refused: bool,
    error: String,
    offered: Option<(String, String)>,
}

/// The longest link taken from outside the app. A real one is far shorter.
const OFFER_MAX: usize = 2048;

/// What the "This computer" screen shows.
#[derive(Clone, Debug, Default, PartialEq, serde::Serialize)]
pub struct View {
    /// The server this computer is linked to, or empty.
    pub server: String,
    /// The number to compare with the browser's while a link waits, or empty.
    pub check: String,
    /// The server no longer takes this device's token.
    pub refused: bool,
    /// The last thing that went wrong, in words for the user, or empty.
    pub error: String,
    /// How this computer shows in the server's device list.
    pub name: String,
}

/// True for a plain-http server on this computer, which is reached with no
/// network in between.
fn local(base: &str) -> bool {
    let host = base.strip_prefix("http://").unwrap_or_default();
    let host = host.split('/').next().unwrap_or_default();
    let host = match host.strip_prefix('[') {
        Some(v6) => v6.split(']').next().unwrap_or_default(),
        None => host.split(':').next().unwrap_or_default(),
    };
    host == "localhost"
        || host
            .parse::<std::net::IpAddr>()
            .is_ok_and(|ip| ip.is_loopback())
}

pub struct Device {
    keys: Result<Keychain, String>,
    name: String,
    inner: Mutex<Inner>,
    /// Held for the whole of `session` and `unlink`, server calls included,
    /// so no session is made while an unlink runs (#636). Taken before
    /// `inner`, never while holding it.
    one: Mutex<()>,
}

impl Device {
    /// `keys` is the keychain, or why there is none. `name` is how this
    /// computer shows in the device list. Reads the keychain once.
    pub fn new(keys: Result<Keychain, String>, name: &str) -> Self {
        let device = Self {
            keys,
            name: name.to_string(),
            inner: Mutex::default(),
            one: Mutex::default(),
        };
        device.reload();
        device
    }

    fn keys(&self) -> Result<&Keychain, String> {
        self.keys.as_ref().map_err(String::clone)
    }

    /// Reads the keychain again, for when it was not ready the first time.
    /// Returns what it holds.
    pub fn reload(&self) -> Option<Linked> {
        let read = self.keys().and_then(Keychain::load);
        let mut i = self.inner.lock().unwrap();
        match read {
            Ok(linked) => {
                i.linked = linked;
                i.error.clear();
            }
            Err(e) => i.error = e,
        }
        i.refused = false;
        i.linked.clone()
    }

    pub fn linked(&self) -> Option<Linked> {
        self.inner.lock().unwrap().linked.clone()
    }

    pub fn view(&self) -> View {
        let i = self.inner.lock().unwrap();
        View {
            server: i
                .linked
                .as_ref()
                .map(|l| l.base.clone())
                .unwrap_or_default(),
            check: i
                .pending
                .as_ref()
                .map(|p| p.link.check().to_string())
                .unwrap_or_default(),
            refused: i.refused,
            error: i.error.clone(),
            name: self.name.clone(),
        }
    }

    /// The sync client was turned away: the token was revoked, or the server
    /// is another one now. The keychain entry stays until the user unlinks,
    /// since a server that is being set up again answers the same way.
    pub fn set_refused(&self) {
        self.inner.lock().unwrap().refused = true;
    }

    /// A link that came from outside the app: the browser opened a
    /// `clrd://link` address, and any page can make it do that. So it is
    /// only kept for the screen to put in the box. Nothing is sent to the
    /// server it names until the user presses Link, and then it goes through
    /// [`Device::start`] like a pasted one. It never replaces a link that is
    /// set up or waiting.
    pub fn offer(&self, text: &str) {
        let mut i = self.inner.lock().unwrap();
        i.offered = None;
        i.error = if i.linked.is_some() {
            "This computer is already linked. Unlink it first.".into()
        } else if i.pending.is_some() {
            "A link is already waiting. Cancel it first.".into()
        } else if let Some(target) = link::parse(text).filter(|_| text.len() <= OFFER_MAX) {
            // ponytail: one offer at a time; a later one replaces an earlier
            // one the screen has not shown yet.
            i.offered = Some((text.to_string(), target.base));
            // What was wrong before (no keychain, say) is still wrong.
            return;
        } else {
            "That is not a clrd device link. Copy the link from Settings, Devices, Add a device."
                .into()
        };
    }

    /// The offered link and the server it names, once. Gone if this computer
    /// was linked, or started linking, in the meantime.
    pub fn take_offer(&self) -> Option<(String, String)> {
        let mut i = self.inner.lock().unwrap();
        let offer = i.offered.take();
        offer.filter(|_| i.linked.is_none() && i.pending.is_none())
    }

    /// Starts linking from a pasted link. `Ok(false)` means nothing was
    /// sent: the link is plain `http://`, so the token would cross the
    /// network unencrypted, and the user has to say that is fine
    /// (`plain_ok`) first. `install` is this computer's id from
    /// [`link::install_id`].
    pub fn start(&self, text: &str, plain_ok: bool, install: &str) -> Result<bool, String> {
        let done = |r: Result<bool, String>| {
            let mut i = self.inner.lock().unwrap();
            i.error = r.clone().err().unwrap_or_default();
            r
        };
        if self.linked().is_some() {
            return done(Err(
                "This computer is already linked. Unlink it first.".into()
            ));
        }
        let Some(target) = link::parse(text) else {
            return done(Err(
                "That is not a clrd device link. Copy the link from Settings, Devices, Add a device."
                    .into(),
            ));
        };
        if target.base.starts_with("http://") && !local(&target.base) && !plain_ok {
            return done(Ok(false));
        }
        // The keychain has to work before a token is asked for: a token that
        // was approved and then could not be kept would be lost.
        if let Err(e) = self.keys().and_then(Keychain::load) {
            return done(Err(e));
        }
        let link = match Link::start(&target, &self.name, install) {
            Ok(link) => link,
            Err(e) => return done(Err(e.to_string())),
        };
        self.inner.lock().unwrap().pending = Some(Pending {
            link,
            base: target.base,
            asked: None,
        });
        // The first ask binds this app to the code, so the browser can show
        // the number.
        self.poll();
        let i = self.inner.lock().unwrap();
        match i.error.is_empty() {
            true => Ok(true),
            false => Err(i.error.clone()),
        }
    }

    /// Stops waiting on a link. The code runs out on the server by itself.
    pub fn cancel(&self) {
        let mut i = self.inner.lock().unwrap();
        i.pending = None;
        i.error.clear();
    }

    /// Asks the server about a waiting link, at most once per [`POLL`]. The
    /// screen calls this as it refreshes. `Some` the moment this computer
    /// becomes linked: the token is in the keychain by then.
    pub fn poll(&self) -> Option<Linked> {
        let mut i = self.inner.lock().unwrap();
        let p = i.pending.as_mut()?;
        if p.asked.is_some_and(|t| t.elapsed() < POLL) {
            return None;
        }
        p.asked = Some(Instant::now());
        // ponytail: the lock is held for the call (30 s at most on a server
        // that hangs), so the screen waits with it. Only this screen asks.
        let token = match p.link.claim() {
            Ok(Claim::Waiting) => return None,
            // Offline for a moment: keep waiting, say why.
            Err(e @ Error::Retry(..)) => {
                i.error = e.to_string();
                return None;
            }
            Ok(Claim::Approved(token)) => token,
            Ok(Claim::Gone) | Err(Error::Auth) => {
                i.pending = None;
                i.error =
                    "The link was denied, used or ran out. Make a new one and try again.".into();
                return None;
            }
        };
        let linked = Linked {
            base: i.pending.take()?.base,
            token,
            session: String::new(),
        };
        if let Err(e) = self.keys().and_then(|k| k.save(&linked)) {
            // Not kept, so not left valid either.
            let _ = Server::new(&linked.base, &linked.token).unlink();
            i.error = e;
            return None;
        }
        i.error.clear();
        i.refused = false;
        i.linked = Some(linked.clone());
        Some(linked)
    }

    /// A fresh session for the app's window, as the `Set-Cookie` header the
    /// server sent. It replaces the one from the last time on the server and
    /// in the keychain, so there is one window session per computer however
    /// often the app restarts.
    pub fn session(&self) -> Result<String, Error> {
        let _one = self.one.lock().unwrap();
        let Some(mut linked) = self.linked() else {
            return Err(Error::Auth);
        };
        let old = (!linked.session.is_empty()).then_some(linked.session.as_str());
        let cookie = match Server::new(&linked.base, &linked.token).session(&self.name, old) {
            Ok(cookie) => cookie,
            Err(e) => {
                if matches!(e, Error::Auth) {
                    self.set_refused();
                }
                return Err(e);
            }
        };
        linked.session = cookie.clone();
        let mut i = self.inner.lock().unwrap();
        // Unlinked while the server answered: the new session died with the
        // token, and nothing is kept.
        if i.linked.as_ref().map(|l| &l.token) != Some(&linked.token) {
            return Err(Error::Auth);
        }
        // ponytail: if the keychain refuses this write, the session is not
        // replaced next time and stays listed until the user removes it or
        // unlinks (the server also caps sessions per device).
        if let Err(e) = self.keys().and_then(|k| k.save(&linked)) {
            i.error = e;
        }
        i.linked = Some(linked);
        Ok(cookie)
    }

    /// Unlinks this computer: ends the window's session, revokes the token
    /// on the server and forgets both here. The keychain entry goes even
    /// when the server cannot be told; the screen then says the device may
    /// still be listed there.
    pub fn unlink(&self) {
        let _one = self.one.lock().unwrap();
        let Some(linked) = self.inner.lock().unwrap().linked.take() else {
            return;
        };
        let server = Server::new(&linked.base, &linked.token);
        // First and by itself: a token without the read scope cannot unlink
        // (#609), and the window session must not outlive the link here.
        if !linked.session.is_empty() {
            let _ = server.end_session(&linked.session);
        }
        let told = server.unlink();
        let cleared = self.keys().and_then(Keychain::clear);
        let mut i = self.inner.lock().unwrap();
        i.refused = false;
        i.error = match (cleared, told) {
            (Err(e), _) => {
                // Still in the keychain, so still linked as far as the next
                // start can tell. Say so instead of pretending.
                i.linked = Some(linked);
                e
            }
            (Ok(()), Ok(())) => String::new(),
            (Ok(()), Err(_)) => format!(
                "Unlinked on this computer, but {} could not be told. It may still list this device: remove it there under Settings, Devices.",
                linked.base
            ),
        };
    }
}

#[cfg(test)]
mod tests {
    #[test]
    fn only_this_computer_counts_as_local() {
        for base in [
            "http://localhost",
            "http://localhost:8080",
            "http://127.0.0.1:8080",
            "http://[::1]:8080/sub",
        ] {
            assert!(super::local(base), "{base}");
        }
        for base in [
            "http://192.168.1.5:8080",
            "http://localhost.example.com",
            "http://127.example.com",
            "http://clrd.example.com",
        ] {
            assert!(!super::local(base), "{base}");
        }
    }
}
