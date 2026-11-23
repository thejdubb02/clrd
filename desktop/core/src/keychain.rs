//! Where this device's token lives: the OS keychain, and nowhere else.
//!
//! Linux: the Secret Service on the session bus (GNOME Keyring, KWallet,
//! KeePassXC). Windows: Credential Manager. There is no fallback to a file:
//! with no keychain the app says so and stays unlinked.
//!
//! One entry holds the server address and the token together, so a token
//! can never be read back and sent to another server.

/// The server address and the token, as one keychain secret.
fn pack(base: &str, token: &str) -> Vec<u8> {
    format!("{base}\n{token}").into_bytes()
}

fn unpack(secret: &[u8]) -> Option<(String, String)> {
    let (base, token) = std::str::from_utf8(secret).ok()?.split_once('\n')?;
    Some((base.to_string(), token.to_string()))
}

#[cfg(target_os = "linux")]
mod os {
    use super::{pack, unpack};
    use std::collections::HashMap;
    use std::pin::Pin;
    use zbus::export::futures_core::Stream;
    use zbus::OwnedVariant as OwnedValue;
    use zbus::{block_on, Connection, MessageStream, ObjectPath, OwnedObjectPath, Variant};

    const NAME: &str = "org.freedesktop.secrets";
    const SERVICE: &str = "org.freedesktop.Secret.Service";
    const COLLECTION: &str = "org.freedesktop.Secret.Collection";
    const ITEM: &str = "org.freedesktop.Secret.Item";
    const PROMPT: &str = "org.freedesktop.Secret.Prompt";
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
        /// for the user. `/` means there is nothing to ask.
        // ponytail: waits as long as the dialog is open, with no timeout of
        // our own; a keyring service that dies mid-prompt holds this thread.
        fn prompt(&self, prompt: &OwnedObjectPath) -> Result<(), String> {
            if prompt.as_str() == "/" {
                return Ok(());
            }
            let err = |e: zbus::Error| format!("keychain: {e}");
            let mut done = block_on(async {
                let rule = zbus::MatchRule::builder()
                    .msg_type(zbus::message::Type::Signal)
                    .interface(PROMPT)
                    .member("Completed")
                    .path(prompt.as_str())
                    .build()?;
                MessageStream::for_match_rule(rule, &self.0, None).await
            })
            .map_err(err)?;
            self.call::<_, ()>(prompt.as_str(), PROMPT, "Prompt", &"")?;
            let msg = block_on(std::future::poll_fn(|cx| Pin::new(&mut done).poll_next(cx)))
                .ok_or("keychain: the bus closed")?
                .map_err(err)?;
            match msg.body().deserialize::<(bool, OwnedValue)>() {
                Ok((false, _)) => Ok(()),
                _ => Err("keychain: stayed locked".into()),
            }
        }

        /// A session for passing secrets, and the default keyring unlocked.
        // ponytail: the "plain" session sends the secret unencrypted over the
        // user's own session bus, like libsecret does when a service offers
        // nothing else. A program that can read that bus can already ask the
        // keychain for the secret itself.
        fn open(&self) -> Result<OwnedObjectPath, String> {
            let (_, session): (OwnedValue, OwnedObjectPath) = self.call(
                "/org/freedesktop/secrets",
                SERVICE,
                "OpenSession",
                &("plain", Variant::from("")),
            )?;
            let (_, prompt): (Vec<OwnedObjectPath>, OwnedObjectPath) = self.call(
                "/org/freedesktop/secrets",
                SERVICE,
                "Unlock",
                &(vec![ObjectPath::from_static_str_unchecked(DEFAULT)],),
            )?;
            self.prompt(&prompt)?;
            Ok(session)
        }

        fn items(&self) -> Result<Vec<OwnedObjectPath>, String> {
            self.call(
                DEFAULT,
                COLLECTION,
                "SearchItems",
                &(HashMap::from([("application", "clrd")]),),
            )
        }

        pub fn save(&self, base: &str, token: &str) -> Result<(), String> {
            let session = self.open()?;
            let mut props: HashMap<&str, Variant<'_>> = HashMap::new();
            props.insert("org.freedesktop.Secret.Item.Label", "clrd".into());
            props.insert(
                "org.freedesktop.Secret.Item.Attributes",
                HashMap::from([("application", "clrd")]).into(),
            );
            let secret: Secret = (session, vec![], pack(base, token), "text/plain".into());
            let (_, prompt): (OwnedObjectPath, OwnedObjectPath) =
                self.call(DEFAULT, COLLECTION, "CreateItem", &(props, secret, true))?;
            self.prompt(&prompt)
        }

        /// `None` when this computer is not linked.
        pub fn load(&self) -> Result<Option<(String, String)>, String> {
            let session = self.open()?;
            let Some(item) = self.items()?.into_iter().next() else {
                return Ok(None);
            };
            let (secret,): (Secret,) = self.call(item.as_str(), ITEM, "GetSecret", &(session,))?;
            Ok(unpack(&secret.2))
        }

        pub fn clear(&self) -> Result<(), String> {
            self.open()?;
            for item in self.items()? {
                let prompt: OwnedObjectPath = self.call(item.as_str(), ITEM, "Delete", &())?;
                self.prompt(&prompt)?;
            }
            Ok(())
        }
    }
}

#[cfg(windows)]
mod os {
    use super::{pack, unpack};
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

        pub fn save(&self, base: &str, token: &str) -> Result<(), String> {
            let (mut name, mut blob) = (target(), pack(base, token));
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
        pub fn load(&self) -> Result<Option<(String, String)>, String> {
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
                let blob =
                    std::slice::from_raw_parts(c.CredentialBlob, c.CredentialBlobSize as usize);
                let out = unpack(blob);
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

pub use os::Keychain;

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_secret_round_trips_and_a_broken_one_is_nothing() {
        let packed = pack("https://clrd.example.com", "clrd_abc");
        assert_eq!(
            unpack(&packed),
            Some(("https://clrd.example.com".into(), "clrd_abc".into()))
        );
        assert_eq!(unpack(b"no newline"), None);
        assert_eq!(unpack(&[0xff, b'\n', 0xff]), None);
    }
}
