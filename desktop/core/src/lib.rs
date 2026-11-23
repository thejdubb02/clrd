//! clrd desktop core: everything that is not a window.
//!
//! The contract is `docs/SPEC.md`. [`engine`] is the sync model of section 3
//! as a state machine with no I/O beyond its state file; [`client`] holds the
//! socket and the REST calls and feeds it.

pub mod client;
pub mod engine;

pub use client::{run, Error, Server, Status};
pub use engine::{Engine, Next, Notification, Sink};
