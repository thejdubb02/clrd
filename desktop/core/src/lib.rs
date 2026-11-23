//! clrd desktop core: everything that is not a window.
//!
//! The contract is `docs/SPEC.md`. [`engine`] is the sync model of section 3
//! as a state machine with no I/O beyond its state file; [`client`] holds the
//! socket and the REST calls and feeds it; `notify` puts the result on the
//! Linux desktop. [`link`] makes this computer a linked device and
//! [`keychain`] keeps its token.

pub mod client;
pub mod engine;
#[cfg(any(target_os = "linux", windows))]
pub mod keychain;
pub mod link;
#[cfg(target_os = "linux")]
pub mod notify;

pub use client::{run, Error, Server, Status};
pub use engine::{Engine, Next, Notification, Sink};
