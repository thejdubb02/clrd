//! A fake clrd server for tests: the event log, replay, `ready`, `reset`,
//! the epoch, the open list and dismiss, as `docs/SPEC.md` sections 3 and 5
//! describe them. It can go offline, be pruned and be swapped for a new
//! database, which is what the sync client has to survive.
#![allow(dead_code)]

use clrd_core::{Engine, Notification, Server, Sink, Status};
use serde_json::{json, Value};
use std::collections::BTreeMap;
use std::io::{Read, Write};
use std::net::{Shutdown, TcpListener, TcpStream};
use std::path::PathBuf;
use std::sync::atomic::{AtomicBool, AtomicU32, Ordering};
use std::sync::{mpsc, Arc, Mutex};
use std::time::{Duration, Instant};
use tungstenite::protocol::Role;
use tungstenite::{Message, WebSocket};

pub const TOKEN: &str = "clrd_testtoken0000000000000000000000";

#[derive(Default)]
struct Inner {
    online: bool,
    epoch: String,
    seq: u64,
    floor: u64, // events at or below this are pruned
    events: Vec<(u64, Value)>,
    rows: BTreeMap<String, Value>,
    live: Vec<mpsc::Sender<String>>,
    conns: Vec<TcpStream>,
    requests: Vec<String>, // every request line, to prove what was and was not sent
    heads: Vec<String>,    // the same requests with their headers
    dismissed: Vec<(String, String)>, // (id, via) of every dismiss call
    version: Option<String>, // None: a server from before /api/v1/version
    minted: u32,
    sessions: Vec<String>, // window sessions that still work, as `name=value`
    refuse_unlink: bool,   // unlinking this device answers 403 (#609)
    slow_session: bool,    // a new session waits for an unlink to start
    hold_dismiss: bool,    // a dismiss waits until this is off again
    unlinked: bool,        // the device removed itself: its token is dead
    page: String,          // what /app serves to a signed-in window
    reports: Vec<String>,  // what that page posted to /report
}

impl Inner {
    fn event(&mut self, kind: &str, id: &str) {
        self.seq += 1;
        let row = self.rows.get_mut(id).unwrap();
        row["seq"] = json!(self.seq);
        let ev = json!({"seq": self.seq, "type": kind, "notification": row});
        self.live.retain(|tx| tx.send(ev.to_string()).is_ok());
        self.events.push((self.seq, ev));
    }

    fn drop_conns(&mut self) {
        self.live.clear();
        for c in self.conns.drain(..) {
            let _ = c.shutdown(Shutdown::Both);
        }
    }
}

#[derive(Clone)]
pub struct Fake {
    pub base: String,
    inner: Arc<Mutex<Inner>>,
}

impl Fake {
    pub fn start() -> Fake {
        let listener = TcpListener::bind("127.0.0.1:0").unwrap();
        let fake = Fake {
            base: format!("http://{}", listener.local_addr().unwrap()),
            inner: Arc::new(Mutex::new(Inner {
                online: true,
                epoch: "e1".into(),
                ..Inner::default()
            })),
        };
        let inner = fake.inner.clone();
        std::thread::spawn(move || {
            for conn in listener.incoming().flatten() {
                let inner = inner.clone();
                std::thread::spawn(move || serve(conn, inner));
            }
        });
        fake
    }

    pub fn publish(&self, id: &str, extra: Value) {
        let mut i = self.inner.lock().unwrap();
        let mut row = json!({"id": id, "topic": "t", "title": id, "message": "m", "priority": 3, "state": "active"});
        row.as_object_mut()
            .unwrap()
            .extend(extra.as_object().cloned().unwrap_or_default());
        i.rows.insert(id.into(), row);
        i.event("created", id);
    }

    /// Another device changed the state.
    pub fn set_state(&self, id: &str, state: &str) {
        let mut i = self.inner.lock().unwrap();
        i.rows.get_mut(id).unwrap()["state"] = json!(state);
        i.event("state", id);
    }

    pub fn repeat(&self, id: &str) {
        self.inner.lock().unwrap().event("repeat", id);
    }

    /// A message with no notification, as marks, settings and future types send.
    pub fn send_raw(&self, kind: &str) {
        let mut i = self.inner.lock().unwrap();
        i.seq += 1;
        let ev = json!({"seq": i.seq, "type": kind});
        i.live.retain(|tx| tx.send(ev.to_string()).is_ok());
        let seq = i.seq;
        i.events.push((seq, ev));
    }

    pub fn offline(&self) {
        let mut i = self.inner.lock().unwrap();
        i.online = false;
        i.drop_conns();
    }

    pub fn online(&self) {
        self.inner.lock().unwrap().online = true;
    }

    /// Retention ran: nothing at or below the current seq can be replayed.
    pub fn prune(&self) {
        let mut i = self.inner.lock().unwrap();
        i.floor = i.seq;
        i.events.clear();
    }

    /// The server was reinstalled: a new epoch, an empty log, seq from `seq`.
    pub fn new_database(&self, seq: u64) {
        let mut i = self.inner.lock().unwrap();
        i.epoch = "e2".into();
        i.seq = seq;
        i.floor = 0;
        i.events.clear();
        i.rows.clear();
        i.drop_conns();
    }

    pub fn seq(&self) -> u64 {
        self.inner.lock().unwrap().seq
    }

    pub fn requests(&self) -> Vec<String> {
        self.inner.lock().unwrap().requests.clone()
    }

    pub fn dismissed(&self) -> Vec<(String, String)> {
        self.inner.lock().unwrap().dismissed.clone()
    }

    /// Every request so far, headers included.
    pub fn heads(&self) -> Vec<String> {
        self.inner.lock().unwrap().heads.clone()
    }

    /// What `/api/v1/version` says. Unset, the fake is an older server
    /// without that endpoint.
    pub fn set_version(&self, version: &str) {
        self.inner.lock().unwrap().version = Some(version.into());
    }

    /// The window sessions that still work, as `clrd_session=value`.
    pub fn sessions(&self) -> Vec<String> {
        self.inner.lock().unwrap().sessions.clone()
    }

    /// Whether unlinking this device answers 403 from now on, as an older
    /// server does for a token without the read scope.
    pub fn refuse_unlink(&self, on: bool) {
        self.inner.lock().unwrap().refuse_unlink = on;
    }

    /// A new session is held back until an unlink starts, or for a second
    /// when none does: the gap a session could slip through (#636).
    pub fn slow_session(&self) {
        self.inner.lock().unwrap().slow_session = true;
    }

    /// While on, a dismiss gets no answer (for five seconds at most): a slow
    /// server, with the app's next signal waiting behind it (#685).
    pub fn hold_dismiss(&self, on: bool) {
        self.inner.lock().unwrap().hold_dismiss = on;
    }

    /// True once the device removed itself.
    pub fn unlinked(&self) -> bool {
        self.inner.lock().unwrap().unlinked
    }

    /// The page `/app` serves to a signed-in window.
    pub fn set_page(&self, html: &str) {
        self.inner.lock().unwrap().page = html.into();
    }

    /// What that page posted to `/report`.
    pub fn reports(&self) -> Vec<String> {
        self.inner.lock().unwrap().reports.clone()
    }
}

fn send(conn: &mut TcpStream, status: &str, headers: &str, body: &str) {
    let _ = write!(
        conn,
        "HTTP/1.1 {status}\r\n{headers}Content-Length: {}\r\nConnection: close\r\n\r\n{body}",
        body.len()
    );
}

fn respond(conn: &mut TcpStream, status: &str, body: &str) {
    send(conn, status, "Content-Type: application/json\r\n", body);
}

/// The routes a real server answers without the device's token: the version,
/// the window's session and the pages a window loads.
fn open_routes(
    conn: &mut TcpStream,
    i: &mut Inner,
    method: &str,
    path: &str,
    cookie: Option<&str>,
    body: &[u8],
) -> bool {
    const HTML: &str = "Content-Type: text/html; charset=utf-8\r\n";
    let signed_in = cookie.is_some_and(|c| i.sessions.iter().any(|s| s == c));
    match (method, path) {
        ("GET", "/api/v1/version") => match &i.version {
            Some(v) => respond(conn, "200 OK", &json!({"version": v}).to_string()),
            None => respond(conn, "404 Not Found", r#"{"error":"not_found"}"#),
        },
        ("POST", "/api/v1/session") => {
            let token = serde_json::from_slice::<Value>(body)
                .ok()
                .and_then(|v| v["token"].as_str().map(String::from));
            if i.unlinked || token.as_deref() != Some(TOKEN) {
                respond(conn, "401 Unauthorized", r#"{"error":"unauthorized"}"#);
                return true;
            }
            i.sessions.retain(|s| Some(s.as_str()) != cookie);
            i.minted += 1;
            let pair = format!("clrd_session=clrd_fakesession{:020}", i.minted);
            i.sessions.push(pair.clone());
            let headers = format!(
                "Content-Type: application/json\r\nSet-Cookie: {pair}; Path=/; Max-Age=31536000; HttpOnly; SameSite=Lax\r\n"
            );
            send(conn, "200 OK", &headers, "{}");
        }
        ("DELETE", "/api/v1/session") => {
            i.sessions.retain(|s| Some(s.as_str()) != cookie);
            respond(conn, "200 OK", "{}");
        }
        ("GET", "/app") if signed_in => send(conn, "200 OK", HTML, &i.page.clone()),
        // Signed out, the real server answers the same address with its
        // sign-in page.
        ("GET", "/app") => send(conn, "200 OK", HTML, "<title>sign in</title>"),
        ("POST", "/report") => {
            i.reports.push(String::from_utf8_lossy(body).to_string());
            respond(conn, "200 OK", "{}");
        }
        _ => return false,
    }
    true
}

fn serve(mut conn: TcpStream, inner: Arc<Mutex<Inner>>) {
    if !inner.lock().unwrap().online {
        return; // dropped: the client sees a dead server
    }
    // Read the head a byte at a time, so nothing after it is swallowed.
    let mut head = Vec::new();
    let mut b = [0u8; 1];
    while !head.ends_with(b"\r\n\r\n") {
        if conn.read(&mut b).unwrap_or(0) == 0 {
            return;
        }
        head.push(b[0]);
    }
    let head = String::from_utf8_lossy(&head).to_string();
    let line = head.lines().next().unwrap_or("").to_string();
    let header = |name: &str| {
        head.lines().find_map(|l| {
            l.split_once(':')
                .filter(|(k, _)| k.eq_ignore_ascii_case(name))
                .map(|(_, v)| v.trim().to_string())
        })
    };
    {
        let mut i = inner.lock().unwrap();
        i.requests.push(line.clone());
        i.heads.push(head.clone());
    }
    let mut parts = line.split(' ');
    let (method, target) = (parts.next().unwrap_or(""), parts.next().unwrap_or(""));
    let (path, query) = target.split_once('?').unwrap_or((target, ""));
    let mut body = vec![
        0u8;
        header("content-length")
            .and_then(|v| v.parse().ok())
            .unwrap_or(0)
    ];
    if conn.read_exact(&mut body).is_err() {
        return;
    }
    let cookie = header("cookie");
    if (method, path) == ("POST", "/api/v1/session") && inner.lock().unwrap().slow_session {
        let until = Instant::now() + Duration::from_secs(1);
        let unlinking = |i: &Inner| {
            i.requests
                .iter()
                .any(|r| r.starts_with("DELETE /api/v1/devices/this "))
        };
        while !unlinking(&inner.lock().unwrap()) && Instant::now() < until {
            std::thread::sleep(Duration::from_millis(10));
        }
    }
    if path.ends_with("/dismiss") {
        let until = Instant::now() + Duration::from_secs(5);
        while inner.lock().unwrap().hold_dismiss && Instant::now() < until {
            std::thread::sleep(Duration::from_millis(10));
        }
    }
    if open_routes(
        &mut conn,
        &mut inner.lock().unwrap(),
        method,
        path,
        cookie.as_deref(),
        &body,
    ) {
        return;
    }
    if inner.lock().unwrap().unlinked
        || header("authorization").as_deref() != Some(&format!("Bearer {TOKEN}"))
    {
        return respond(&mut conn, "401 Unauthorized", r#"{"error":"unauthorized"}"#);
    }

    if path == "/api/v1/ws" {
        let since: u64 = query
            .split('&')
            .find_map(|p| p.strip_prefix("since="))
            .and_then(|s| s.parse().ok())
            .unwrap_or(0);
        let key = header("sec-websocket-key").unwrap_or_default();
        let accept = tungstenite::handshake::derive_accept_key(key.as_bytes());
        let _ = write!(conn, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: {accept}\r\n\r\n");
        let handle = conn.try_clone().unwrap();
        let mut ws = WebSocket::from_raw_socket(conn, Role::Server, None);
        let (tx, rx) = mpsc::channel::<String>();
        {
            // Replay and subscribe under one lock, so no event falls between.
            let mut i = inner.lock().unwrap();
            if since > i.seq || since < i.floor {
                let _ = ws.send(Message::text(json!({"type": "reset"}).to_string()));
            } else {
                for (seq, ev) in &i.events {
                    if *seq > since {
                        let _ = ws.send(Message::text(ev.to_string()));
                    }
                }
            }
            let _ = ws.send(Message::text(
                json!({"type": "settings", "settings": {}}).to_string(),
            ));
            let _ = ws.send(Message::text(
                json!({"type": "ready", "seq": i.seq, "epoch": i.epoch}).to_string(),
            ));
            i.live.push(tx);
            i.conns.push(handle);
        }
        while let Ok(text) = rx.recv() {
            if ws.send(Message::text(text)).is_err() {
                break;
            }
        }
        return;
    }

    let mut i = inner.lock().unwrap();
    if path == "/api/v1/devices/this" && method == "DELETE" {
        if i.refuse_unlink {
            return respond(&mut conn, "403 Forbidden", r#"{"error":"forbidden"}"#);
        }
        // The token dies, and every window session under it.
        i.unlinked = true;
        i.sessions.clear();
        i.drop_conns();
        return respond(&mut conn, "200 OK", "{}");
    }
    if method == "GET" && path == "/api/v1/notifications" {
        let open: Vec<&Value> = i
            .rows
            .values()
            .filter(|r| {
                matches!(
                    r["state"].as_str(),
                    Some("active" | "acknowledged" | "snoozed")
                )
            })
            .collect();
        let body = json!({"notifications": open, "seq": i.seq, "epoch": i.epoch}).to_string();
        return respond(&mut conn, "200 OK", &body);
    }
    if let Some(id) = path
        .strip_prefix("/api/v1/notifications/")
        .and_then(|p| p.strip_suffix("/dismiss"))
        .filter(|_| method == "POST")
    {
        if !i.rows.contains_key(id) {
            return respond(&mut conn, "404 Not Found", r#"{"error":"not_found"}"#);
        }
        let via = serde_json::from_slice::<Value>(&body)
            .ok()
            .and_then(|v| v["via"].as_str().map(String::from))
            .unwrap_or_default();
        i.dismissed.push((id.into(), via));
        i.rows.get_mut(id).unwrap()["state"] = json!("dismissed");
        i.event("state", id);
        let body = i.rows[id].to_string();
        return respond(&mut conn, "200 OK", &body);
    }
    respond(&mut conn, "404 Not Found", r#"{"error":"not_found"}"#);
}

/// A sink that writes down what it was told, like `show a alert`, `show a quiet`, `close a`.
#[derive(Clone, Default)]
pub struct Recorder(pub Arc<Mutex<Vec<String>>>);

impl Sink for Recorder {
    fn show(&mut self, n: &Notification, alert: bool) {
        self.0.lock().unwrap().push(format!(
            "show {} {}",
            n.id,
            if alert { "alert" } else { "quiet" }
        ));
    }
    fn close(&mut self, id: &str) {
        self.0.lock().unwrap().push(format!("close {id}"));
    }
}

impl Recorder {
    pub fn log(&self) -> Vec<String> {
        self.0.lock().unwrap().clone()
    }

    pub fn clear(&self) {
        self.0.lock().unwrap().clear();
    }
}

static DIRS: AtomicU32 = AtomicU32::new(0);

/// A fresh state file path for one test.
pub fn state_path() -> PathBuf {
    let dir = std::env::temp_dir().join(format!(
        "clrd-core-test-{}-{}",
        std::process::id(),
        DIRS.fetch_add(1, Ordering::Relaxed)
    ));
    std::fs::create_dir_all(&dir).unwrap();
    dir.join("state.json")
}

/// A session bus of our own, gone when the test ends: the process and its
/// address.
#[cfg(target_os = "linux")]
pub struct Bus(pub std::process::Child, pub String);

#[cfg(target_os = "linux")]
impl Bus {
    pub fn start() -> Bus {
        use std::io::BufRead;
        // A config with no service folders, so this bus never starts a
        // program installed on the machine (a real keyring, say) by name.
        let config = state_path().with_file_name("bus.conf");
        std::fs::write(
            &config,
            r#"<busconfig><type>session</type><listen>unix:tmpdir=/tmp</listen>
<policy context="default"><allow send_destination="*" eavesdrop="true"/>
<allow eavesdrop="true"/><allow own="*"/></policy></busconfig>"#,
        )
        .unwrap();
        let mut child = std::process::Command::new("dbus-daemon")
            .arg(format!("--config-file={}", config.display()))
            .args(["--nofork", "--print-address"])
            .stdout(std::process::Stdio::piped())
            .spawn()
            .expect("dbus-daemon is needed for these tests");
        let mut address = String::new();
        std::io::BufReader::new(child.stdout.take().unwrap())
            .read_line(&mut address)
            .unwrap();
        Bus(child, address.trim().to_string())
    }

    pub fn connect(&self) -> zbus::Connection {
        zbus::block_on(zbus::connection::Builder::address(self.1.as_str()).build()).unwrap()
    }
}

#[cfg(target_os = "linux")]
impl Drop for Bus {
    fn drop(&mut self) {
        let _ = self.0.kill();
        let _ = self.0.wait();
    }
}

/// A child process that ends with the test.
pub struct Kill(pub std::process::Child);

impl Drop for Kill {
    fn drop(&mut self) {
        let _ = self.0.kill();
        let _ = self.0.wait();
    }
}

/// True when any file under `dir` holds `needle` as plain bytes.
pub fn on_disk(dir: &std::path::Path, needle: &[u8]) -> bool {
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
#[cfg(target_os = "linux")]
pub fn keyring(bus: &Bus, home: &std::path::Path) -> Kill {
    use std::process::Stdio;
    let mut daemon = std::process::Command::new("gnome-keyring-daemon")
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

/// A keyring of its own that already holds the link to `fake`, and the home
/// directory it keeps its files under.
#[cfg(target_os = "linux")]
pub fn linked(fake: &Fake) -> (Bus, Kill, clrd_core::keychain::Keychain, PathBuf) {
    use clrd_core::keychain::{Keychain, Linked};
    let home = state_path().with_file_name("home");
    std::fs::create_dir_all(home.join("run")).unwrap();
    let bus = Bus::start();
    let daemon = keyring(&bus, &home);
    let keys = Keychain::new(bus.connect());
    wait("keyring up", || keys.load().is_ok());
    keys.save(&Linked {
        base: fake.base.clone(),
        token: TOKEN.into(),
        session: String::new(),
    })
    .unwrap();
    (bus, daemon, keys, home)
}

/// A sync client running on its own thread, as the app runs it.
pub struct Client {
    pub log: Recorder,
    pub status: Arc<Mutex<Vec<Status>>>,
    pub engine: Arc<Mutex<Engine<Recorder>>>,
    stop: Arc<AtomicBool>,
    thread: Option<std::thread::JoinHandle<Result<(), clrd_core::Error>>>,
}

impl Client {
    pub fn start(base: &str, path: &std::path::Path, token: &str) -> Client {
        let log = Recorder::default();
        let engine = Arc::new(Mutex::new(
            Engine::open(path.to_path_buf(), log.clone()).unwrap(),
        ));
        let status = Arc::new(Mutex::new(Vec::new()));
        let stop = Arc::new(AtomicBool::new(false));
        let server = Server::new(base, token);
        let (e, s, st) = (engine.clone(), stop.clone(), status.clone());
        let thread = std::thread::spawn(move || {
            clrd_core::run(&server, &e, &s, |x| st.lock().unwrap().push(x))
        });
        Client {
            log,
            status,
            engine,
            stop,
            thread: Some(thread),
        }
    }

    /// Stops the client and returns what `run` returned.
    pub fn stop(&mut self) -> Result<(), clrd_core::Error> {
        self.stop.store(true, Ordering::Relaxed);
        self.thread.take().unwrap().join().unwrap()
    }

    pub fn last_status(&self) -> Option<Status> {
        self.status.lock().unwrap().last().copied()
    }
}

impl Drop for Client {
    fn drop(&mut self) {
        self.stop.store(true, Ordering::Relaxed);
    }
}

/// Waits up to 10 s for `cond`, then fails with `what`.
pub fn wait(what: &str, cond: impl Fn() -> bool) {
    let until = Instant::now() + Duration::from_secs(10);
    while !cond() {
        assert!(Instant::now() < until, "timed out waiting for: {what}");
        std::thread::sleep(Duration::from_millis(20));
    }
}

/// Long enough for a wrong event to have arrived, for "this never happens" checks.
pub fn settle() {
    std::thread::sleep(Duration::from_millis(400));
}
