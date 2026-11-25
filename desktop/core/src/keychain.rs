//! Where this device's token lives: the OS keychain, and nowhere else.
//!
//! Linux: the Secret Service on the session bus (GNOME Keyring, KWallet,
//! KeePassXC). Windows: Credential Manager. There is no fallback to a file:
//! with no keychain the app says so and stays unlinked.
//!
//! One entry holds the server address and the token together, so a token
//! can never be read back and sent to another server.

/// What the keychain holds for a linked computer.
#[derive(Clone, Default, PartialEq)]
pub struct Linked {
    /// The server's address.
    pub base: String,
    /// This device's token.
    pub token: String,
    /// The window's session, as the `Set-Cookie` header the server sent, or
    /// empty. Kept so a restart can replace that session on the server
    /// instead of leaving it behind. A secret like the token: the web view's
    /// own cookie store never gets to write it to disk.
    pub session: String,
}

// Written by hand so neither secret can reach a log through `{:?}`.
impl std::fmt::Debug for Linked {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "Linked({})", self.base)
    }
}

/// All of it as one keychain secret, a line each.
fn pack(l: &Linked) -> Vec<u8> {
    format!("{}\n{}\n{}", l.base, l.token, l.session).into_bytes()
}

/// An entry from before the session was kept has two lines.
fn unpack(secret: &[u8]) -> Option<Linked> {
    let mut lines = std::str::from_utf8(secret).ok()?.split('\n');
    Some(Linked {
        base: lines.next()?.to_string(),
        token: lines.next()?.to_string(),
        session: lines.next().unwrap_or_default().to_string(),
    })
}

#[cfg(target_os = "linux")]
mod os {
    use super::{pack, unpack, Linked};
    use std::collections::HashMap;
    use std::pin::Pin;
    use std::task::Poll;
    use zbus::export::futures_core::Stream;
    use zbus::OwnedVariant as OwnedValue;
    use zbus::{block_on, Connection, MessageStream, OwnedObjectPath, Variant};

    const NAME: &str = "org.freedesktop.secrets";
    const SERVICE: &str = "org.freedesktop.Secret.Service";
    const COLLECTION: &str = "org.freedesktop.Secret.Collection";
    const ITEM: &str = "org.freedesktop.Secret.Item";
    const PROMPT: &str = "org.freedesktop.Secret.Prompt";
    const SESSION: &str = "org.freedesktop.Secret.Session";
    const DBUS: &str = "org.freedesktop.DBus";
    /// The keyring the desktop unlocks at login.
    const DEFAULT: &str = "/org/freedesktop/secrets/aliases/default";

    /// What a secret looks like on the bus: session, parameters, value, type.
    type Secret = (OwnedObjectPath, Vec<u8>, Vec<u8>, String);

    pub struct Keychain(Connection);

    impl Keychain {
        /// `conn` is the session bus.
        pub fn new(conn: Connection) -> Self {
            Self(conn)
        }

        fn call<B, R>(&self, path: &str, iface: &str, method: &str, body: &B) -> Result<R, String>
        where
            B: serde::Serialize + zbus::DynamicType,
            R: for<'d> zbus::zvariant::DynamicDeserialize<'d>,
        {
            block_on(
                self.0
                    .call_method(Some(NAME), path, Some(iface), method, body),
            )
            .and_then(|reply| reply.body().deserialize::<R>())
            .map_err(|e| format!("keychain: {e}"))
        }

        /// Runs a prompt the service asked for (the unlock dialog) and waits
        /// for the user, as long as the dialog is open. `/` means there is
        /// nothing to ask. Only the service itself can end the wait, and it
        /// ends with an error if the service goes away first (#608).
        fn prompt(&self, prompt: &OwnedObjectPath) -> Result<(), String> {
            if prompt.as_str() == "/" {
                return Ok(());
            }
            let err = |e: zbus::Error| format!("keychain: {e}");
            let (mut done, mut gone) = block_on(async {
                let owner: String = self
                    .0
                    .call_method(
                        Some(DBUS),
                        "/org/freedesktop/DBus",
                        Some(DBUS),
                        "GetNameOwner",
                        &NAME,
                    )
                    .await?
                    .body()
                    .deserialize()?;
                let rule = zbus::MatchRule::builder()
                    .msg_type(zbus::message::Type::Signal)
                    .sender(owner.as_str())
                    .interface(PROMPT)
                    .member("Completed")
                    .path(prompt.as_str())
                    .build()?;
                let done = MessageStream::for_match_rule(rule, &self.0, None).await?;
                let rule = zbus::MatchRule::builder()
                    .msg_type(zbus::message::Type::Signal)
                    .sender(DBUS)
                    .interface(DBUS)
                    .member("NameOwnerChanged")
                    .arg(0, NAME)
                    .build()?;
                let gone = MessageStream::for_match_rule(rule, &self.0, None).await?;
                Ok::<_, zbus::Error>((done, gone))
            })
            .map_err(err)?;
            self.call::<_, ()>(prompt.as_str(), PROMPT, "Prompt", &"")?;
            let msg = block_on(std::future::poll_fn(|cx| {
                match Pin::new(&mut done).poll_next(cx) {
                    Poll::Pending => Pin::new(&mut gone).poll_next(cx).map(|_| None),
                    ready => ready,
                }
            }))
            .ok_or("keychain: the keyring went away during the unlock")?
            .map_err(err)?;
            match msg.body().deserialize::<(bool, OwnedValue)>() {
                Ok((false, _)) => Ok(()),
                _ => Err("keychain: stayed locked".into()),
            }
        }

        /// Unlocks `paths`, asking the user if the service wants to. A
        /// service may lock entries one by one, not only whole keyrings.
        fn unlock(&self, paths: &[OwnedObjectPath]) -> Result<(), String> {
            let (_, prompt): (Vec<OwnedObjectPath>, OwnedObjectPath) =
                self.call("/org/freedesktop/secrets", SERVICE, "Unlock", &(paths,))?;
            self.prompt(&prompt)
        }

        /// Runs `f` with the default keyring unlocked and a session for
        /// passing secrets, closed again after.
        // ponytail: the "plain" session sends the secret unencrypted over the
        // user's own session bus, like libsecret does when a service offers
        // nothing else. A program that can read that bus can already ask the
        // keychain for the secret itself.
        fn with_session<R>(
            &self,
            f: impl FnOnce(&OwnedObjectPath) -> Result<R, String>,
        ) -> Result<R, String> {
            let (_, session): (OwnedValue, OwnedObjectPath) = self.call(
                "/org/freedesktop/secrets",
                SERVICE,
                "OpenSession",
                &("plain", Variant::from("")),
            )?;
            let out = self
                .unlock(&[DEFAULT.try_into().unwrap()])
                .and_then(|()| f(&session));
            // Tidiness only: a session also ends with the bus connection.
            let _ = self.call::<_, ()>(session.as_str(), SESSION, "Close", &());
            out
        }

        fn items(&self) -> Result<Vec<OwnedObjectPath>, String> {
            self.call(
                DEFAULT,
                COLLECTION,
                "SearchItems",
                &(HashMap::from([("application", "clrd")]),),
            )
        }

        pub fn save(&self, linked: &Linked) -> Result<(), String> {
            self.with_session(|session| {
                let mut props: HashMap<&str, Variant<'_>> = HashMap::new();
                props.insert("org.freedesktop.Secret.Item.Label", "clrd".into());
                props.insert(
                    "org.freedesktop.Secret.Item.Attributes",
                    HashMap::from([("application", "clrd")]).into(),
                );
                let secret: Secret = (session.clone(), vec![], pack(linked), "text/plain".into());
                let (_, prompt): (OwnedObjectPath, OwnedObjectPath) =
                    self.call(DEFAULT, COLLECTION, "CreateItem", &(props, secret, true))?;
                self.prompt(&prompt)
            })
        }

        /// `None` when this computer is not linked.
        pub fn load(&self) -> Result<Option<Linked>, String> {
            self.with_session(|session| loop {
                let Some(item) = self.items()?.into_iter().next() else {
                    return Ok(None);
                };
                let read = self.unlock(std::slice::from_ref(&item)).and_then(|()| {
                    self.call::<_, (Secret,)>(item.as_str(), ITEM, "GetSecret", &(session,))
                });
                match read {
                    Ok((secret,)) => return Ok(unpack(&secret.2)),
                    // Still there, so the read itself failed.
                    Err(e) if self.items()?.contains(&item) => return Err(e),
                    // Removed between the search and the read (an unlink in
                    // another process): look again. Ends, as each turn needs
                    // one more entry to have gone.
                    Err(_) => {}
                }
            })
        }

        pub fn clear(&self) -> Result<(), String> {
            self.unlock(&[DEFAULT.try_into().unwrap()])?;
            let items = self.items()?;
            self.unlock(&items)?;
            for item in items {
                let prompt: OwnedObjectPath = self.call(item.as_str(), ITEM, "Delete", &())?;
                self.prompt(&prompt)?;
            }
            Ok(())
        }
    }
}

#[cfg(windows)]
mod os {
    use super::{pack, unpack, Linked};
    use windows_sys::Win32::Foundation::{GetLastError, ERROR_NOT_FOUND};
    use windows_sys::Win32::Security::Credentials::{
        CredDeleteW, CredFree, CredReadW, CredWriteW, CREDENTIALW, CRED_PERSIST_LOCAL_MACHINE,
        CRED_TYPE_GENERIC,
    };

    #[derive(Default)]
    pub struct Keychain;

    fn target() -> Vec<u16> {
        "clrd\0".encode_utf16().collect()
    }

    fn failed(what: &str) -> String {
        format!("keychain: {what} failed ({})", unsafe { GetLastError() })
    }

    impl Keychain {
        pub fn new() -> Self {
            Self
        }

        pub fn save(&self, linked: &Linked) -> Result<(), String> {
            let (mut name, mut blob) = (target(), pack(linked));
            // SAFETY: CREDENTIALW is plain data, and zero is valid for every
            // field left unset.
            let mut cred: CREDENTIALW = unsafe { std::mem::zeroed() };
            cred.Type = CRED_TYPE_GENERIC;
            cred.TargetName = name.as_mut_ptr();
            cred.CredentialBlobSize = blob.len() as u32;
            cred.CredentialBlob = blob.as_mut_ptr();
            cred.Persist = CRED_PERSIST_LOCAL_MACHINE;
            // SAFETY: `cred` points at `name` and `blob`, alive for the call.
            match unsafe { CredWriteW(&cred, 0) } {
                0 => Err(failed("write")),
                _ => Ok(()),
            }
        }

        /// `None` when this computer is not linked.
        pub fn load(&self) -> Result<Option<Linked>, String> {
            let mut cred: *mut CREDENTIALW = std::ptr::null_mut();
            // SAFETY: on success `cred` is a credential Windows allocated,
            // read here and then freed with CredFree.
            unsafe {
                if CredReadW(target().as_ptr(), CRED_TYPE_GENERIC, 0, &mut cred) == 0 {
                    return match GetLastError() {
                        ERROR_NOT_FOUND => Ok(None),
                        _ => Err(failed("read")),
                    };
                }
                let c = &*cred;
                // An empty credential may come with a null pointer, which a
                // slice must never be built from.
                let out = if c.CredentialBlob.is_null() {
                    None
                } else {
                    unpack(std::slice::from_raw_parts(
                        c.CredentialBlob,
                        c.CredentialBlobSize as usize,
                    ))
                };
                CredFree(cred.cast());
                Ok(out)
            }
        }

        pub fn clear(&self) -> Result<(), String> {
            // SAFETY: the target name is a NUL-ended string alive for the call.
            unsafe {
                if CredDeleteW(target().as_ptr(), CRED_TYPE_GENERIC, 0) == 0
                    && GetLastError() != ERROR_NOT_FOUND
                {
                    return Err(failed("delete"));
                }
            }
            Ok(())
        }
    }
}

#[cfg(any(target_os = "linux", windows))]
pub use os::Keychain;

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_secret_round_trips_and_a_broken_one_is_nothing() {
        let linked = Linked {
            base: "https://clrd.example.com".into(),
            token: "clrd_abc".into(),
            session: "clrd_session=s; Path=/; HttpOnly".into(),
        };
        assert!(unpack(&pack(&linked)) == Some(linked.clone()));
        // An entry saved before the session was kept still loads.
        let old = unpack(b"https://clrd.example.com\nclrd_abc").unwrap();
        assert!(old.token == "clrd_abc" && old.session.is_empty());
        // Neither secret prints.
        assert_eq!(format!("{linked:?}"), "Linked(https://clrd.example.com)");
        assert_eq!(unpack(b"no newline"), None);
        assert_eq!(unpack(&[0xff, b'\n', 0xff]), None);
    }
}
