//! One app per session, on a private bus.
#![cfg(target_os = "linux")]

mod common;

use clrd_core::instance::claim;
use common::{wait, Bus};
use std::sync::atomic::{AtomicU32, Ordering};
use std::sync::{Arc, Mutex};

#[test]
fn a_second_start_asks_the_first_to_open_and_leaves() {
    let bus = Bus::start();
    let opened = Arc::new(AtomicU32::new(0));
    let links = Arc::new(Mutex::new(Vec::new()));

    let first = bus.connect();
    let (count, got) = (opened.clone(), links.clone());
    assert!(claim(&first, "", move |link| {
        got.lock().unwrap().push(link);
        count.fetch_add(1, Ordering::Relaxed);
    })
    .unwrap());
    assert_eq!(opened.load(Ordering::Relaxed), 0);

    // The second carries a link, which has to arrive as it was sent.
    let link = "clrd://link?server=https%3A%2F%2Fclrd.example.com%2Fsub&code=x1";
    for (starts, sent) in [(1, ""), (2, link)] {
        let later = bus.connect();
        assert!(!claim(&later, sent, |_| panic!("the later start is not the app")).unwrap());
        wait("the first app is asked to open", || {
            opened.load(Ordering::Relaxed) == starts
        });
    }
    assert_eq!(*links.lock().unwrap(), ["", link]);
}
