//! One running app per desktop session (Linux).
//!
//! The first app to start owns a name on the session bus. A second start
//! finds the name taken, asks the first to open its window, and leaves.
//! Without this, two apps would each show every notification and each hold
//! a window session.

use std::pin::Pin;
use std::time::Duration;
use zbus::export::futures_core::Stream;
use zbus::fdo::RequestNameFlags;
use zbus::{block_on, Connection, MessageStream};

const NAME: &str = "org.clrd.Desktop";
const PATH: &str = "/org/clrd/Desktop";

/// The session bus, with a limit on how long any one call may take, so a
/// service that stops answering cannot hold a thread for good.
pub fn session_bus() -> zbus::Result<Connection> {
    block_on(
        zbus::connection::Builder::session()
            .method_timeout(Duration::from_secs(30))
            .build(),
    )
}

/// `true` when this process is now the one app; it keeps the name for as
/// long as `conn` lives, and `on_open` runs each time a later start asks for
/// the window, with the `clrd://` link that start was given, or an empty
/// string. `false` when another app already runs: it has been asked to open
/// and handed `link`, and this process should exit.
pub fn claim(
    conn: &Connection,
    link: &str,
    on_open: impl Fn(String) + Send + 'static,
) -> zbus::Result<bool> {
    block_on(async {
        // Listening starts before the name is asked for, so no request can
        // fall between the two.
        // ponytail: any program on the user's own session bus can send this
        // signal. All it does is show a window, and put a link in the box
        // for the user to look at: the app treats the link as outside input.
        let rule = zbus::MatchRule::builder()
            .msg_type(zbus::message::Type::Signal)
            .interface(NAME)
            .member("Open")
            .path(PATH)
            .build()?;
        let mut stream = MessageStream::for_match_rule(rule, conn, None).await?;
        match conn
            .request_name_with_flags(NAME, RequestNameFlags::DO_NOT_QUEUE)
            .await
        {
            Ok(_) => {
                std::thread::spawn(move || {
                    while let Some(Ok(msg)) = block_on(std::future::poll_fn(|cx| {
                        Pin::new(&mut stream).poll_next(cx)
                    })) {
                        // No text in it still means "open".
                        on_open(msg.body().deserialize::<String>().unwrap_or_default());
                    }
                });
                Ok(true)
            }
            Err(zbus::Error::NameTaken) => {
                // To the app alone, not to everyone on the bus: the link
                // holds a link code.
                conn.emit_signal(Some(NAME), PATH, NAME, "Open", &link)
                    .await?;
                Ok(false)
            }
            Err(e) => Err(e),
        }
    })
}
