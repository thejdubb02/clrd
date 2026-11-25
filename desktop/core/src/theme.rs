//! Whether the desktop asks for light or dark: the settings portal's colour
//! scheme (`org.freedesktop.appearance` `color-scheme`).

use std::pin::Pin;
use std::time::Duration;
use zbus::export::futures_core::Stream;
use zbus::{block_on, MessageStream, Variant};

const NAME: &str = "org.freedesktop.portal.Desktop";
const PATH: &str = "/org/freedesktop/portal/desktop";
const SETTINGS: &str = "org.freedesktop.portal.Settings";
const KEY: (&str, &str) = ("org.freedesktop.appearance", "color-scheme");

/// Tells `on` what the desktop asks for (1 dark, 2 light, 0 no preference):
/// once before this returns when the portal answers, then from a thread of
/// its own at each change. A desktop with no portal is never heard from, and
/// `on` is never called.
pub fn follow(on: impl Fn(u32) + Send + 'static) {
    // The first answer is waited for, so a window opens in the right colours.
    // Two seconds at most: a portal that hangs must not hold the start.
    let Ok(conn) = block_on(
        zbus::connection::Builder::session()
            .method_timeout(Duration::from_secs(2))
            .build(),
    ) else {
        return;
    };
    // Listening starts before the first read, so no change falls between.
    let Ok(mut changes) = block_on(async {
        // From the portal alone, not from any program on the bus.
        let rule = zbus::MatchRule::builder()
            .msg_type(zbus::message::Type::Signal)
            .sender(NAME)
            .interface(SETTINGS)
            .member("SettingChanged")
            .path(PATH)
            .build()?;
        MessageStream::for_match_rule(rule, &conn, None).await
    }) else {
        return;
    };
    let read = block_on(conn.call_method(Some(NAME), PATH, Some(SETTINGS), "Read", &KEY));
    // `Read` wraps its answer twice.
    if let Ok(reply) = read {
        let body = reply.body();
        if let Ok(Variant::Variant(scheme)) = body.deserialize::<Variant>() {
            if let Variant::U32(scheme) = *scheme {
                on(scheme);
            }
        }
    }
    std::thread::spawn(move || {
        while let Some(Ok(msg)) = block_on(std::future::poll_fn(|cx| {
            Pin::new(&mut changes).poll_next(cx)
        })) {
            let body = msg.body();
            if let Ok((namespace, key, Variant::U32(scheme))) =
                body.deserialize::<(&str, &str, Variant)>()
            {
                if (namespace, key) == KEY {
                    on(scheme);
                }
            }
        }
    });
}
