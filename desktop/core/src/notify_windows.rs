//! The Windows side of `notify`: toasts, which also sit in the notification
//! centre after they leave the screen.
//!
//! Windows shows a toast only for an app it knows by a Start menu shortcut
//! carrying the app's id, which the installer makes. A build run from the
//! source tree has none: each toast is then refused, logged and skipped.

use super::{toast_xml, Signal};
use crate::engine::Notification;
use std::collections::HashMap;
use std::sync::atomic::{AtomicBool, AtomicU32, Ordering};
use std::sync::{mpsc, Arc, Mutex};
use windows::core::{h, IInspectable, Interface, Ref, Result, HSTRING};
use windows::Data::Xml::Dom::XmlDocument;
use windows::Foundation::TypedEventHandler;
use windows::UI::Notifications::{
    ToastActivatedEventArgs, ToastDismissalReason, ToastDismissedEventArgs, ToastNotification,
    ToastNotificationManager,
};

/// All our toasts are in one group, so a tag is only ever matched to ours.
const GROUP: &HSTRING = h!("clrd");

/// Our id for a toast to its tag (the clrd id) and the toast itself, which
/// has to be kept for its events to arrive.
type Toasts = Arc<Mutex<HashMap<u32, (HSTRING, ToastNotification)>>>;

pub struct Os {
    app_id: HSTRING,
    close_clears: Arc<AtomicBool>,
    tx: mpsc::Sender<Signal>,
    /// Windows gives a toast no number, so we count. Each showing gets a
    /// new one: an event from a toast that has since been replaced then
    /// matches nothing.
    last: AtomicU32,
    toasts: Toasts,
}

impl Os {
    pub fn new(app_id: &str, close_clears: Arc<AtomicBool>, tx: mpsc::Sender<Signal>) -> Self {
        let os = Self {
            app_id: app_id.into(),
            close_clears,
            tx,
            last: AtomicU32::new(0),
            toasts: Arc::default(),
        };
        // What an earlier run left in the notification centre: nothing
        // listens to those any more, and the open ones are shown again.
        os.stop();
        os
    }

    /// Shows `n`, in place of the toast we know as `replaces` (0 for none),
    /// and gives our id for the new one.
    pub fn notify(&self, n: &Notification, alert: bool, replaces: u32) -> Result<u32> {
        let os_id = self.last.fetch_add(1, Ordering::Relaxed) + 1;
        let xml = XmlDocument::new()?;
        xml.LoadXml(&toast_xml(n, alert).into())?;
        let toast = ToastNotification::CreateToastNotification(&xml)?;
        // The same tag takes the place of the one before it.
        let tag = HSTRING::from(n.id.as_str());
        toast.SetTag(&tag)?;
        toast.SetGroup(GROUP)?;
        // A quiet one goes straight to the notification centre.
        toast.SetSuppressPopup(!alert)?;

        // Windows calls these on threads of its own: they only hand on.
        let (tx, clears) = (self.tx.clone(), self.close_clears.clone());
        let toasts = Arc::downgrade(&self.toasts);
        toast.Dismissed(&TypedEventHandler::new(
            move |_, args: Ref<ToastDismissedEventArgs>| {
                // Only a close by the user counts. One that timed out is
                // still in the notification centre, and still ours.
                if args.ok()?.Reason()? == ToastDismissalReason::UserCanceled {
                    if let Some(toasts) = toasts.upgrade() {
                        toasts.lock().unwrap().remove(&os_id);
                    }
                    // The setting as it is now, as the close arrives (#685).
                    let _ = tx.send(Signal::Closed(os_id, clears.load(Ordering::Relaxed)));
                }
                Ok(())
            },
        ))?;
        let tx = self.tx.clone();
        toast.Activated(&TypedEventHandler::new(
            move |_, args: Ref<IInspectable>| {
                // The Clear button says "clear"; a click on the toast says
                // what `launch` holds, and anything but "clear" opens.
                let key = args
                    .ok()
                    .and_then(|a| a.cast::<ToastActivatedEventArgs>())
                    .and_then(|a| a.Arguments())
                    .map(|a| a.to_string())
                    .unwrap_or_default();
                let _ = tx.send(Signal::Action(os_id, key));
                Ok(())
            },
        ))?;

        ToastNotificationManager::CreateToastNotifierWithId(&self.app_id)?.Show(&toast)?;
        let mut toasts = self.toasts.lock().unwrap();
        toasts.remove(&replaces);
        toasts.insert(os_id, (tag, toast));
        Ok(os_id)
    }

    /// Takes the toast off the screen and out of the notification centre.
    pub fn close(&self, os_id: u32) {
        let Some((tag, _)) = self.toasts.lock().unwrap().remove(&os_id) else {
            return;
        };
        let _ = ToastNotificationManager::History()
            .and_then(|h| h.RemoveGroupedTagWithId(&tag, GROUP, &self.app_id));
    }

    /// Takes down everything of ours.
    // ponytail: a toast clicked while the app is not running does nothing.
    // Opening the app from it needs a COM activator registered by the
    // installer; a start clears the old ones instead.
    pub fn stop(&self) {
        self.toasts.lock().unwrap().clear();
        let _ = ToastNotificationManager::History().and_then(|h| h.ClearWithId(&self.app_id));
    }
}
