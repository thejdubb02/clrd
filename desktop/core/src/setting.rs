//! The one setting this computer keeps for itself: "Closing clears
//! everywhere" (`docs/SPEC.md` section 8.1), off by default.

use std::io;
use std::path::PathBuf;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex};

/// On while its file is there: nothing to parse, and a fresh install is off.
pub struct CloseClears {
    path: PathBuf,
    on: Arc<AtomicBool>,
    /// One change at a time, so the file and the flag never disagree.
    saving: Mutex<()>,
}

impl CloseClears {
    pub fn open(path: PathBuf) -> Self {
        let on = Arc::new(AtomicBool::new(path.exists()));
        CloseClears {
            path,
            on,
            saving: Mutex::new(()),
        }
    }

    /// What the notifier reads at each close, so a change counts at once.
    pub fn flag(&self) -> Arc<AtomicBool> {
        self.on.clone()
    }

    pub fn get(&self) -> bool {
        self.on.load(Ordering::Relaxed)
    }

    /// Kept first, then used: a change that cannot be kept is not made, so
    /// what the switch shows is what the next start does.
    pub fn set(&self, on: bool) -> io::Result<()> {
        let _one = self.saving.lock().unwrap();
        let kept = if on {
            std::fs::write(&self.path, "")
        } else {
            std::fs::remove_file(&self.path)
        };
        match kept {
            Err(e) if on || e.kind() != io::ErrorKind::NotFound => return Err(e),
            _ => self.on.store(on, Ordering::Relaxed),
        }
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn off_until_turned_on_and_kept_across_starts() {
        let dir = std::env::temp_dir().join(format!("clrd-setting-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        let path = dir.join("close-clears");

        let first = CloseClears::open(path.clone());
        let flag = first.flag();
        assert!(!first.get(), "off on a fresh install");
        first.set(false).unwrap();
        first.set(true).unwrap();
        assert!(flag.load(Ordering::Relaxed), "the notifier sees it at once");
        assert!(
            CloseClears::open(path.clone()).get(),
            "kept for the next start"
        );
        first.set(false).unwrap();
        assert!(!flag.load(Ordering::Relaxed));
        assert!(!CloseClears::open(path).get());

        // A change that cannot be kept is not made: a file is in the way of
        // the folder here.
        std::fs::write(dir.join("gone"), "").unwrap();
        let lost = CloseClears::open(dir.join("gone").join("close-clears"));
        assert!(lost.set(true).is_err());
        assert!(!lost.get());
        std::fs::remove_dir_all(dir).unwrap();
    }
}
