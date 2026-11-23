//! The sync model of `docs/SPEC.md` section 3: apply events in order, keep
//! the highest seq and the server's epoch, and start over from the open list
//! when the server says the cursor is no good.

use serde::{Deserialize, Serialize};
use std::collections::BTreeMap;
use std::io::{self, Write};
use std::path::PathBuf;

/// The fields of a notification (SPEC section 2) this app uses. The rest is
/// ignored, and a field missing from an old event takes its default.
#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct Notification {
    pub id: String,
    pub topic: String,
    pub title: String,
    pub message: String,
    pub priority: u8,
    pub state: String,
    pub silenced: bool,
    pub click: String,
    pub seq: u64,
}

impl Notification {
    /// Active and acknowledged rows show; snoozed and final ones do not. A
    /// state this version does not know hides, so nothing is stuck on screen.
    fn shows(&self) -> bool {
        matches!(self.state.as_str(), "active" | "acknowledged")
    }

    /// Only an active row that no silence matched may make a sound.
    fn may_alert(&self) -> bool {
        self.state == "active" && !self.silenced
    }
}

/// Where notifications go on this computer.
pub trait Sink {
    /// Show `n`, or replace what is showing for its id. `alert` is false for
    /// a quiet update: no sound, no new popup.
    fn show(&mut self, n: &Notification, alert: bool);
    /// Take down what is showing for `id`.
    fn close(&mut self, id: &str);
}

/// What the caller does after a message.
#[derive(Debug, PartialEq)]
pub enum Next {
    Continue,
    /// The replay is applied and the stream is live.
    Ready,
    /// The cursor belongs to nothing the server has (a `reset`, or another
    /// epoch): fetch the open list, call [`Engine::replace_all`], reconnect.
    Reset,
}

#[derive(Default, Serialize, Deserialize)]
#[serde(default)]
struct State {
    seq: u64,
    epoch: String,
    /// What is showing, by id, so a restart can take down what was cleared
    /// while the app was closed.
    open: BTreeMap<String, Notification>,
}

#[derive(Deserialize)]
struct Msg {
    #[serde(default, rename = "type")]
    kind: String,
    #[serde(default)]
    seq: u64,
    #[serde(default)]
    epoch: String,
    notification: Option<Notification>,
}

pub struct Engine<S: Sink> {
    state: State,
    path: PathBuf,
    pub sink: S,
    replaying: bool,
    // A replay is held back until `ready`, where the epoch is checked: only
    // then is it known to come from the database the cursor belongs to. One
    // copy per id, so a notification made and cleared while this computer
    // was offline never pops up.
    // ponytail: the whole replay sits in memory; the server's retention
    // bounds it. Apply in batches if that ever gets large.
    buffer: BTreeMap<String, Notification>,
    buffer_seq: u64,
}

fn bad(e: impl std::fmt::Display) -> io::Error {
    io::Error::new(io::ErrorKind::InvalidData, e.to_string())
}

impl<S: Sink> Engine<S> {
    /// Loads the state file (a missing one is a first run) and shows what
    /// was open, quietly: the last run already alerted for it.
    pub fn open(path: PathBuf, mut sink: S) -> io::Result<Self> {
        let state: State = match std::fs::read(&path) {
            Ok(b) => serde_json::from_slice(&b).map_err(bad)?,
            Err(e) if e.kind() == io::ErrorKind::NotFound => State::default(),
            Err(e) => return Err(e),
        };
        for n in state.open.values() {
            sink.show(n, false);
        }
        Ok(Self {
            state,
            path,
            sink,
            replaying: true,
            buffer: BTreeMap::new(),
            buffer_seq: 0,
        })
    }

    /// The seq to connect with.
    pub fn seq(&self) -> u64 {
        self.state.seq
    }

    /// A new socket: what an unfinished replay left behind is dropped, and
    /// the server sends it again.
    pub fn connecting(&mut self) {
        self.replaying = true;
        self.buffer.clear();
        self.buffer_seq = 0;
    }

    /// One text frame from the stream. An error means the frame could not be
    /// read or saved: the caller must reconnect, never skip it, or the
    /// cursor would move past an event that was not applied.
    pub fn handle(&mut self, text: &str) -> io::Result<Next> {
        let m: Msg = serde_json::from_str(text).map_err(bad)?;
        match m.kind.as_str() {
            "created" | "updated" | "state" | "repeat" => {
                let n = m
                    .notification
                    .filter(|n| !n.id.is_empty())
                    .ok_or_else(|| bad("event without a notification"))?;
                let seq = m.seq.max(n.seq);
                if self.replaying {
                    self.buffer_seq = self.buffer_seq.max(seq);
                    self.buffer.insert(n.id.clone(), n);
                } else {
                    // Only a live repeat rings again; one missed while
                    // offline is old news.
                    self.apply(n, m.kind == "repeat");
                    self.state.seq = self.state.seq.max(seq);
                    self.save()?;
                }
            }
            // A mark changes nothing this app shows and never alerts; it
            // only moves the cursor.
            "mark" => {
                if self.replaying {
                    self.buffer_seq = self.buffer_seq.max(m.seq);
                } else if m.seq > self.state.seq {
                    self.state.seq = m.seq;
                    self.save()?;
                }
            }
            "ready" => {
                if !m.epoch.is_empty()
                    && !self.state.epoch.is_empty()
                    && m.epoch != self.state.epoch
                {
                    self.buffer.clear();
                    return Ok(Next::Reset);
                }
                if !m.epoch.is_empty() {
                    self.state.epoch = m.epoch;
                }
                for (_, n) in std::mem::take(&mut self.buffer) {
                    self.apply(n, false);
                }
                // Events this token may not see still move the cursor, or a
                // limited token would fall behind retention.
                self.state.seq = self.state.seq.max(self.buffer_seq).max(m.seq);
                self.replaying = false;
                self.save()?;
                return Ok(Next::Ready);
            }
            "reset" => {
                self.buffer.clear();
                return Ok(Next::Reset);
            }
            _ => {} // settings, devices and whatever comes later
        }
        Ok(Next::Continue)
    }

    /// The server's open list is the truth: what it no longer lists comes
    /// down, and the cursor and epoch are the list's.
    pub fn replace_all(
        &mut self,
        open: Vec<Notification>,
        seq: u64,
        epoch: String,
    ) -> io::Result<()> {
        let stays = |id: &String| open.iter().any(|n| n.shows() && &n.id == id);
        for id in self.state.open.keys().filter(|id| !stays(id)) {
            self.sink.close(id);
        }
        // What stays is kept, so apply() knows it is showing and stays quiet.
        self.state.open.retain(|id, _| stays(id));
        for n in open {
            self.apply(n, false);
        }
        self.state.seq = seq;
        self.state.epoch = epoch;
        self.save()
    }

    fn apply(&mut self, n: Notification, repeat: bool) {
        let old = self.state.open.remove(&n.id);
        if !n.shows() {
            if old.is_some() {
                self.sink.close(&n.id);
            }
            return;
        }
        let was_active = old.is_some_and(|o| o.state == "active");
        self.sink.show(&n, n.may_alert() && (!was_active || repeat));
        self.state.open.insert(n.id.clone(), n);
    }

    /// Written whole to a new file and renamed over the old one, so a crash
    /// leaves either the old state or the new, never half of one.
    fn save(&self) -> io::Result<()> {
        let tmp = self.path.with_extension("tmp");
        let mut o = std::fs::OpenOptions::new();
        o.write(true).create(true).truncate(true);
        #[cfg(unix)]
        std::os::unix::fs::OpenOptionsExt::mode(&mut o, 0o600); // notification text is the user's own
        let mut f = o.open(&tmp)?;
        f.write_all(&serde_json::to_vec(&self.state).map_err(bad)?)?;
        f.sync_all()?;
        std::fs::rename(&tmp, &self.path)
    }
}
