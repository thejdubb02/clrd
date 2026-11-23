//! Updates for the installed app (#719).
//!
//! The build is given an address and a public key. The address serves a small
//! JSON feed (the shape Tauri's updater uses) that names the newest version,
//! where its installer is, and the installer's minisign signature. An
//! installer is kept and run only when that signature verifies against the
//! key and is for the very version the feed names, and that version is newer
//! than the running one. A build with no address or no key never asks.
//!
//! Nothing here knows the linked server or its token: the requests are made
//! by a client of their own and carry no sign-in of any kind.

use std::collections::HashMap;
use std::path::PathBuf;
use std::time::Duration;

use base64::Engine as _;
use minisign_verify::{PublicKey, Signature};

/// The feed is a few hundred bytes and the installer about 1.5 MB. Anything
/// far past that is not ours, and is dropped before it fills memory.
const FEED_MAX: u64 = 64 * 1024;
const PACKAGE_MAX: u64 = 64 * 1024 * 1024;

/// The version an automatic update last started the installer for.
const TRIED: &str = "update-tried";

/// Automatic updates are on by default: a build with an address only ever
/// runs what the key signed, and a build without one never asks. This empty
/// file in the app's folder turns them off, which is also how a setup script
/// or an administrator does it.
const NO_AUTO: &str = "no-auto-update";

pub struct Updater {
    /// Where the feed is. Empty: this build has no updates.
    pub url: String,
    /// The minisign public key file, base64, as `tauri signer` prints it.
    pub pubkey: String,
    /// The running version.
    pub running: String,
    /// This system's name in the feed, `windows-x86_64`.
    pub target: String,
    /// The app's own folder: the verified installer and [`TRIED`] live here.
    pub dir: PathBuf,
}

/// A newer version, downloaded and verified.
#[derive(Debug)]
pub struct Update {
    pub version: String,
    pub file: PathBuf,
}

#[derive(serde::Deserialize)]
struct Feed {
    version: String,
    platforms: HashMap<String, Package>,
}

#[derive(serde::Deserialize)]
struct Package {
    signature: String,
    url: String,
}

impl Updater {
    /// Whether this build was given somewhere to ask and a key to check with.
    pub fn enabled(&self) -> bool {
        !self.url.is_empty() && !self.pubkey.is_empty()
    }

    /// The Check for updates button: asks whatever the switch says.
    pub fn check(&self) -> Result<Option<Update>, String> {
        self.fetch("")
    }

    /// The "Update automatically" switch. On unless [`NO_AUTO`] is there.
    pub fn auto(&self) -> bool {
        !self.dir.join(NO_AUTO).exists()
    }

    pub fn set_auto(&self, on: bool) -> std::io::Result<()> {
        let file = self.dir.join(NO_AUTO);
        match if on {
            std::fs::remove_file(file)
        } else {
            std::fs::write(file, "")
        } {
            Err(e) if !on || e.kind() != std::io::ErrorKind::NotFound => Err(e),
            _ => Ok(()),
        }
    }

    /// The timer's check. With the switch off it asks nothing at all. A
    /// version whose installer was already started once is not fetched
    /// again, so an install that keeps failing cannot restart the app in a
    /// loop; the button still tries it.
    pub fn background(&self) -> Option<Update> {
        if !self.auto() {
            return None;
        }
        let tried = std::fs::read_to_string(self.dir.join(TRIED)).unwrap_or_default();
        self.fetch(tried.trim()).ok().flatten()
    }

    fn fetch(&self, skip: &str) -> Result<Option<Update>, String> {
        if !self.enabled() {
            return Ok(None);
        }
        let key = PublicKey::decode(&text(&self.pubkey)?).map_err(|e| e.to_string())?;
        let feed: Feed = serde_json::from_slice(&get(&self.url, FEED_MAX)?)
            .map_err(|e| format!("the update feed could not be read: {e}"))?;
        if feed.version == skip || !newer(&feed.version, &self.running) {
            return Ok(None);
        }
        let package = feed
            .platforms
            .get(&self.target)
            .ok_or("the update feed has no package for this system")?;
        let signature = Signature::decode(&text(&package.signature)?).map_err(|e| e.to_string())?;
        // The feed itself is not signed, so its version alone proves nothing:
        // a feed could name an old, properly signed installer as "9.9.9" to
        // take the app back. The signer writes the version into the signed
        // comment, and that one has to agree.
        let signed = signature
            .trusted_comment()
            .split('\t')
            .find_map(|field| field.strip_prefix("version:"));
        if signed != Some(feed.version.as_str()) {
            return Err("the update is signed as another version".into());
        }
        let bytes = get(&package.url, PACKAGE_MAX)?;
        // Checks the file and the comment read above, both against the key.
        key.verify(&bytes, &signature, false)
            .map_err(|_| "the update's signature does not match")?;
        let file = self.dir.join("clrd-update.exe");
        std::fs::write(&file, bytes).map_err(|e| format!("the update could not be kept: {e}"))?;
        Ok(Some(Update {
            version: feed.version,
            file,
        }))
    }

    /// Starts the verified installer: no questions (`/P`), as an update
    /// (`/UPDATE`, which keeps the shortcuts as they are), and the app
    /// started again in the tray once it has succeeded (`/R`). The caller
    /// quits once this returns, so the installer can replace the running
    /// file. An installer that fails starts nothing: the app stays closed
    /// until it is opened again.
    pub fn install(&self, update: &Update) -> std::io::Result<()> {
        // Kept first: an installer that was started is never started by the
        // timer a second time.
        let tried = self.dir.join(TRIED);
        std::fs::write(&tried, &update.version)?;
        std::process::Command::new(&update.file)
            .args(["/P", "/UPDATE", "/R", "/ARGS", "--hidden"])
            .spawn()
            .map(drop)
            // One that never started restarts nothing, so the timer may try
            // it again, say once a virus scanner has let the file go.
            .inspect_err(|_| drop(std::fs::remove_file(tried)))
    }
}

/// The key and each signature are a short text file, sent as base64.
fn text(base64: &str) -> Result<String, String> {
    base64::engine::general_purpose::STANDARD
        .decode(base64.trim())
        .ok()
        .and_then(|bytes| String::from_utf8(bytes).ok())
        .ok_or_else(|| "the update's key or signature is not readable".into())
}

fn get(url: &str, max: u64) -> Result<Vec<u8>, String> {
    let mut answer = crate::client::agent(Duration::from_secs(120))
        .get(url)
        .config()
        // Downloads are often handed on to a file host.
        .max_redirects(5)
        // An https address is never handed on to plain http, where someone
        // on the network could answer "nothing new" for ever.
        .https_only(url.starts_with("https://"))
        .build()
        .call()
        .map_err(|e| format!("the update server could not be reached: {e}"))?;
    if answer.status() != 200 {
        return Err(format!("the update server answered {}", answer.status()));
    }
    answer
        .body_mut()
        .with_config()
        .limit(max)
        .read_to_vec()
        .map_err(|e| format!("the update could not be read: {e}"))
}

/// Plain dotted numbers only. Anything else is never newer.
// ponytail: no pre-release tags (1.0.0-beta); add when a release has one.
fn newer(version: &str, than: &str) -> bool {
    let parts = |v: &str| {
        v.split('.')
            .map(|n| n.parse().ok())
            .collect::<Option<Vec<u64>>>()
    };
    matches!((parts(version), parts(than)), (Some(a), Some(b)) if a > b)
}

#[cfg(test)]
mod tests {
    #[test]
    fn newer_compares_numbers_not_text() {
        use super::newer;
        assert!(newer("0.10.0", "0.9.0"));
        assert!(newer("1.0.0", "0.9.9"));
        assert!(!newer("0.1.0", "0.1.0"));
        assert!(!newer("0.1.0", "0.2.0"));
        assert!(!newer("9.9.9-beta", "0.1.0"));
        assert!(!newer("", "0.1.0"));
    }
}
