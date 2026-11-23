//! The socket and the REST calls (`docs/SPEC.md` sections 3 and 5).
//!
//! The token goes out only as an `Authorization: Bearer` header. It is never
//! put in a URL (the `?auth=` form the spec keeps for ntfy clients is not
//! used) and never in an error or a `Debug` print.

use crate::engine::{Engine, Next, Notification, Sink};
use serde::Deserialize;
use std::io;
use std::net::{TcpStream, ToSocketAddrs};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Mutex;
use std::time::{Duration, Instant, SystemTime};
use tungstenite::client::IntoClientRequest;
use tungstenite::Message;

const PAGE: usize = 500;
const CONNECT_TIMEOUT: Duration = Duration::from_secs(15);
/// The server pings every 30 s; two missed pings and the line is dead.
const SILENCE: Duration = Duration::from_secs(75);
/// How often a blocked read wakes to look at the stop flag.
const TICK: Duration = Duration::from_millis(250);

#[derive(Debug)]
pub enum Error {
    /// The server refused the token. Retrying cannot fix that.
    Auth,
    /// Anything a retry may fix, with how long the server asked us to wait.
    Retry(String, Option<Duration>),
}

impl std::fmt::Display for Error {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Error::Auth => f.write_str("the server refused this device's token"),
            Error::Retry(why, _) => f.write_str(why),
        }
    }
}

impl std::error::Error for Error {}

pub(crate) fn retry(e: impl std::fmt::Display) -> Error {
    Error::Retry(e.to_string(), None)
}

/// The HTTP client every call here goes through, on the OS's TLS and roots.
pub(crate) fn agent(timeout: Duration) -> ureq::Agent {
    use ureq::tls::{RootCerts, TlsConfig, TlsProvider};
    let tls = TlsConfig::builder()
        .provider(TlsProvider::NativeTls)
        .root_certs(RootCerts::PlatformVerifier)
        .build();
    ureq::Agent::config_builder()
        .timeout_global(Some(timeout))
        // The API never redirects. Following one would turn a DELETE into a
        // GET of some other page and report its 200 as "revoked".
        .max_redirects(0)
        .http_status_as_error(false) // read here, for Retry-After
        .tls_config(tls)
        .build()
        .into()
}

pub(crate) fn status_error(code: u16, retry_after: Option<&str>) -> Error {
    match code {
        401 | 403 => Error::Auth,
        _ => Error::Retry(
            format!("server answered {code}"),
            retry_after
                .and_then(|s| s.trim().parse().ok())
                .map(Duration::from_secs),
        ),
    }
}

/// What the tray shows.
#[derive(Clone, Copy, Debug, PartialEq)]
pub enum Status {
    Connecting,
    Connected,
    Offline,
    AuthFailed,
}

/// A clrd server and this device's token for it.
pub struct Server {
    base: String,
    bearer: String,
    http: ureq::Agent,
}

// Written by hand so the token cannot reach a log through `{:?}`.
impl std::fmt::Debug for Server {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "Server({})", self.base)
    }
}

#[derive(Deserialize)]
struct List {
    notifications: Vec<Notification>,
    seq: u64,
    #[serde(default)]
    epoch: String,
}

impl Server {
    /// `base` is the server's address, like `https://clrd.example.com`.
    pub fn new(base: &str, token: &str) -> Self {
        Self {
            base: base.trim_end_matches('/').to_string(),
            bearer: format!("Bearer {token}"),
            http: agent(Duration::from_secs(30)),
        }
    }

    /// Signs the app's own window in (SPEC section 4, Session): the server
    /// mints a separate session token under this device's and answers with
    /// it as a `Set-Cookie` header, returned as sent. The window gets that
    /// cookie and never this device's token. Treat the value as a secret.
    pub fn session(&self, name: &str) -> Result<String, Error> {
        let token = self.bearer.strip_prefix("Bearer ").unwrap_or_default();
        let res = Self::checked(
            self.http
                .post(format!("{}/api/v1/session", self.base))
                .send_json(serde_json::json!({ "token": token, "name": name })),
        )?;
        res.headers()
            .get_all("set-cookie")
            .iter()
            .filter_map(|v| v.to_str().ok())
            .find(|v| v.starts_with("clrd_session="))
            .map(str::to_string)
            .ok_or_else(|| retry("the server did not answer with a session"))
    }

    /// Signs that window out: revokes the session `cookie` (the header
    /// [`Server::session`] returned) on the server.
    pub fn end_session(&self, cookie: &str) -> Result<(), Error> {
        let pair = cookie.split(';').next().unwrap_or_default();
        Self::checked(
            self.http
                .delete(format!("{}/api/v1/session", self.base))
                .header("Cookie", pair)
                .call(),
        )
        .map(|_| ())
    }

    /// Unlinks this device on the server: its token stops working, and so
    /// does every window session signed in with it.
    pub fn unlink(&self) -> Result<(), Error> {
        #[derive(Deserialize)]
        struct Device {
            id: String,
            #[serde(default)]
            this: bool,
        }
        #[derive(Deserialize)]
        struct Devices {
            devices: Vec<Device>,
        }
        let mut res = Self::checked(
            self.http
                .get(format!("{}/api/v1/devices?all=1", self.base))
                .header("Authorization", &self.bearer)
                .call(),
        )?;
        let list: Devices = res.body_mut().read_json().map_err(retry)?;
        let me = list
            .devices
            .into_iter()
            .find(|d| d.this)
            .ok_or_else(|| retry("the server does not list this device"))?;
        Self::checked(
            self.http
                .delete(format!("{}/api/v1/devices/{}", self.base, me.id))
                .header("Authorization", &self.bearer)
                .call(),
        )
        .map(|_| ())
    }

    fn checked(
        res: Result<ureq::http::Response<ureq::Body>, ureq::Error>,
    ) -> Result<ureq::http::Response<ureq::Body>, Error> {
        let res = res.map_err(retry)?;
        if res.status().is_success() {
            return Ok(res);
        }
        Err(status_error(
            res.status().as_u16(),
            res.headers()
                .get("retry-after")
                .and_then(|v| v.to_str().ok()),
        ))
    }

    /// Every open notification, with the seq and epoch to carry on from.
    pub fn list_open(&self) -> Result<(Vec<Notification>, u64, String), Error> {
        let mut rows: Vec<Notification> = Vec::new();
        let mut head: Option<(u64, String)> = None;
        loop {
            let mut url = format!("{}/api/v1/notifications?state=open&limit={PAGE}", self.base);
            if let Some(last) = rows.last() {
                url += &format!("&before={}", last.id);
            }
            let mut res = Self::checked(
                self.http
                    .get(&url)
                    .header("Authorization", &self.bearer)
                    .call(),
            )?;
            let page: List = res.body_mut().read_json().map_err(retry)?;
            match &head {
                None => head = Some((page.seq, page.epoch)),
                // The seq is the first page's, so a later page from another
                // database cannot be stitched onto it.
                Some((_, epoch)) if *epoch != page.epoch => {
                    return Err(retry("server changed between pages"))
                }
                Some(_) => {}
            }
            let n = page.notifications.len();
            rows.extend(page.notifications);
            if n < PAGE {
                let (seq, epoch) = head.unwrap_or_default();
                return Ok((rows, seq, epoch));
            }
        }
    }

    /// The user cleared `id` on this computer. `via` says how (SPEC section 5).
    pub fn dismiss(&self, id: &str, via: &str) -> Result<(), Error> {
        let url = format!("{}/api/v1/notifications/{id}/dismiss", self.base);
        let body = serde_json::json!({ "via": via });
        Self::checked(
            self.http
                .post(&url)
                .header("Authorization", &self.bearer)
                .send_json(body),
        )
        .map(|_| ())
    }

    fn stream(&self, since: u64) -> Result<Stream, Error> {
        let url = format!(
            "ws{}/api/v1/ws?since={since}",
            self.base
                .strip_prefix("http")
                .ok_or_else(|| retry("server address must start with http:// or https://"))?
        );
        let mut req = url.into_client_request().map_err(retry)?;
        req.headers_mut().insert(
            "Authorization",
            self.bearer.parse().map_err(|_| Error::Auth)?,
        );
        let uri = req.uri();
        let host = uri
            .host()
            .ok_or_else(|| retry("server address has no host"))?;
        let port = uri
            .port_u16()
            .unwrap_or(if uri.scheme_str() == Some("wss") {
                443
            } else {
                80
            });
        // Every address the name resolves to, in order: the first may be an
        // IPv6 route that goes nowhere while the next one works.
        let mut tcp = Err(retry("server address did not resolve"));
        for addr in (host, port).to_socket_addrs().map_err(retry)? {
            tcp = TcpStream::connect_timeout(&addr, CONNECT_TIMEOUT).map_err(retry);
            if tcp.is_ok() {
                break;
            }
        }
        let tcp = tcp?;
        // A second handle on the same socket: timeouts are set through it
        // once TLS owns the first.
        let handle = tcp.try_clone().map_err(retry)?;
        handle
            .set_read_timeout(Some(CONNECT_TIMEOUT))
            .map_err(retry)?;
        handle
            .set_write_timeout(Some(CONNECT_TIMEOUT))
            .map_err(retry)?;
        let (ws, _) = tungstenite::client_tls(req, tcp).map_err(|e| match e {
            tungstenite::HandshakeError::Failure(tungstenite::Error::Http(res)) => status_error(
                res.status().as_u16(),
                res.headers()
                    .get("retry-after")
                    .and_then(|v| v.to_str().ok()),
            ),
            e => retry(e),
        })?;
        handle.set_read_timeout(Some(TICK)).map_err(retry)?;
        Ok(ws)
    }
}

type Stream = tungstenite::WebSocket<tungstenite::stream::MaybeTlsStream<TcpStream>>;

/// Reads one socket until it fails or `stop` is set. `Ok(true)` asks for an
/// immediate reconnect (the open list was just fetched after a reset).
fn follow<S: Sink>(
    server: &Server,
    engine: &Mutex<Engine<S>>,
    stop: &AtomicBool,
    on_status: &mut dyn FnMut(Status),
) -> Result<bool, Error> {
    let since = {
        let mut e = engine.lock().unwrap();
        e.connecting();
        e.seq()
    };
    let mut ws = server.stream(since)?;
    let mut heard = Instant::now();
    loop {
        if stop.load(Ordering::Relaxed) {
            let _ = ws.close(None);
            return Ok(false);
        }
        let msg = match ws.read() {
            Ok(m) => m,
            Err(tungstenite::Error::Io(e))
                if matches!(
                    e.kind(),
                    io::ErrorKind::WouldBlock | io::ErrorKind::TimedOut
                ) =>
            {
                if heard.elapsed() > SILENCE {
                    return Err(retry("server went silent"));
                }
                continue;
            }
            Err(e) => return Err(retry(e)),
        };
        heard = Instant::now();
        match msg {
            Message::Text(text) => {
                // Its own statement, so the lock is let go before the match:
                // the reset arm takes it again.
                let next = engine.lock().unwrap().handle(&text).map_err(retry)?;
                match next {
                    Next::Continue => {}
                    Next::Ready => on_status(Status::Connected),
                    Next::Reset => {
                        // The spec lets this socket carry on after `reset`, but
                        // a fresh connect from the list's seq is simpler and is
                        // also right when the cause is a new epoch.
                        let _ = ws.close(None);
                        let (open, seq, epoch) = server.list_open()?;
                        engine
                            .lock()
                            .unwrap()
                            .replace_all(open, seq, epoch)
                            .map_err(retry)?;
                        return Ok(true);
                    }
                }
            }
            // An unlinked or revoked device is told so in the close reason.
            Message::Close(Some(f)) if f.reason.as_str() == "token revoked" => {
                return Err(Error::Auth)
            }
            Message::Close(_) => return Err(retry("server closed the stream")),
            // The answer to a ping is queued by read(); send it now, since
            // the server drops a stream that does not answer.
            _ => ws.flush().map_err(retry)?,
        }
    }
}

/// Keeps `engine` in sync with `server` until `stop` is set or the token is
/// refused. Reconnects with backoff, from the last seq.
pub fn run<S: Sink>(
    server: &Server,
    engine: &Mutex<Engine<S>>,
    stop: &AtomicBool,
    mut on_status: impl FnMut(Status),
) -> Result<(), Error> {
    let mut attempt = 0u32;
    while !stop.load(Ordering::Relaxed) {
        on_status(Status::Connecting);
        let mut connected = false;
        let mut status = |s| {
            connected |= s == Status::Connected;
            on_status(s);
        };
        let asked = match follow(server, engine, stop, &mut status) {
            Ok(true) => {
                attempt = 0;
                continue;
            }
            Ok(false) => break,
            Err(Error::Auth) => {
                on_status(Status::AuthFailed);
                return Err(Error::Auth);
            }
            Err(Error::Retry(_, asked)) => asked,
        };
        on_status(Status::Offline);
        if connected {
            attempt = 0; // a working connection dropped; that is not a failed try
        }
        // 1 s doubling to 60 s, with jitter so a restarted server is not hit
        // by every device at once, and never sooner than the server asked.
        let cap = Duration::from_millis(1000u64 << attempt.min(6)).min(Duration::from_secs(60));
        attempt += 1;
        let nanos = SystemTime::now()
            .duration_since(SystemTime::UNIX_EPOCH)
            .map_or(0, |d| d.subsec_nanos());
        let wait = (cap / 2 + cap.mul_f64(f64::from(nanos % 1000) / 2000.0))
            .max(asked.unwrap_or_default());
        let until = Instant::now() + wait;
        while Instant::now() < until && !stop.load(Ordering::Relaxed) {
            std::thread::sleep(TICK.min(until - Instant::now()));
        }
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::{Read, Write};

    #[test]
    fn a_redirected_sign_out_is_not_reported_as_done() {
        let listener = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
        let base = format!("http://{}", listener.local_addr().unwrap());
        std::thread::spawn(move || {
            // First answer: a redirect. Anything that follows it gets a 200.
            for (n, conn) in listener.incoming().enumerate() {
                let mut conn = conn.unwrap();
                let _ = conn.read(&mut [0; 2048]);
                let head = if n == 0 {
                    "302 Found\r\nLocation: /login"
                } else {
                    "200 OK"
                };
                let _ = write!(
                    conn,
                    "HTTP/1.1 {head}\r\nContent-Length: 0\r\nConnection: close\r\n\r\n"
                );
            }
        });
        assert!(Server::new(&base, "t")
            .end_session("clrd_session=x")
            .is_err());
    }
}
