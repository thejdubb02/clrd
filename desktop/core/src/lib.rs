//! clrd desktop core: everything that is not a window.
//!
//! The contract is `docs/SPEC.md`. [`engine`] is the sync model of section 3
//! as a state machine with no I/O beyond its state file; [`client`] holds the
//! socket and the REST calls and feeds it; `notify` puts the result on the
//! Linux desktop. [`link`] makes this computer a linked device,
//! [`keychain`] keeps its token, and `device` is the two as the one state
//! the app's screens show. `instance` keeps it to one running app,
//! [`setting`] is the one switch this computer keeps for itself, and `theme`
//! says whether the desktop asks for dark.

pub mod client;
#[cfg(any(target_os = "linux", windows))]
pub mod device;
pub mod engine;
#[cfg(target_os = "linux")]
pub mod instance;
#[cfg(any(target_os = "linux", windows))]
pub mod keychain;
pub mod link;
#[cfg(target_os = "linux")]
pub mod notify;
pub mod setting;
#[cfg(target_os = "linux")]
pub mod theme;

pub use client::{run, Error, Server, Status};
pub use engine::{Engine, Next, Notification, Sink};
