//! One app per session, on a private bus.
#![cfg(target_os = "linux")]

mod common;

use clrd_core::instance::claim;
use common::{wait, Bus};
use std::sync::atomic::{AtomicU32, Ordering};
use std::sync::Arc;

#[test]
fn a_second_start_asks_the_first_to_open_and_leaves() {
    let bus = Bus::start();
    let opened = Arc::new(AtomicU32::new(0));

    let first = bus.connect();
    let count = opened.clone();
    assert!(claim(&first, move || {
        count.fetch_add(1, Ordering::Relaxed);
    })
    .unwrap());
    assert_eq!(opened.load(Ordering::Relaxed), 0);

    for starts in 1..=2 {
        let later = bus.connect();
        assert!(!claim(&later, || panic!("the later start is not the app")).unwrap());
        wait("the first app is asked to open", || {
            opened.load(Ordering::Relaxed) == starts
        });
    }
}
