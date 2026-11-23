//! One running app per sign-in session (Windows).
//!
//! The first app to start makes a named pipe that only one process can make.
//! A second start finds it there, writes its `clrd://` link into it (or
//! nothing), and leaves; the first reads that and opens its window.
//!
//! Pipe names are shared by the whole computer, so the name carries the
//! session's number and a later start checks who it is talking to before it
//! writes: the link holds a link code.

use std::fs::{File, OpenOptions};
use std::io::{Error, Read, Write};
use std::os::windows::fs::OpenOptionsExt;
use std::os::windows::io::{AsRawHandle, FromRawHandle};
use std::ptr::{null, null_mut};
use std::time::Duration;

use windows_sys::Win32::Foundation::{
    GetLastError, ERROR_NO_DATA, ERROR_PIPE_CONNECTED, GENERIC_WRITE, INVALID_HANDLE_VALUE,
};
use windows_sys::Win32::Storage::FileSystem::{
    FILE_FLAG_FIRST_PIPE_INSTANCE, FILE_READ_ATTRIBUTES, PIPE_ACCESS_INBOUND,
    SECURITY_IDENTIFICATION,
};
use windows_sys::Win32::System::Pipes::{
    ConnectNamedPipe, CreateNamedPipeW, DisconnectNamedPipe, GetNamedPipeServerProcessId,
    GetNamedPipeServerSessionId, PIPE_REJECT_REMOTE_CLIENTS,
};
use windows_sys::Win32::System::RemoteDesktop::ProcessIdToSessionId;
use windows_sys::Win32::UI::WindowsAndMessaging::AllowSetForegroundWindow;

/// A link is a few hundred bytes. More than this is not read.
const MAX: u32 = 4096;

/// `true` when this process is now the one app: `on_open` runs each time a
/// later start asks for the window, with the `clrd://` link that start was
/// given, or an empty string. `false` when another app already runs in this
/// session: it has been asked to open and handed `link`, and this process
/// should exit. An error means neither could be done.
pub fn claim(link: &str, on_open: impl Fn(String) + Send + 'static) -> std::io::Result<bool> {
    let mut session = 0;
    if unsafe { ProcessIdToSessionId(std::process::id(), &mut session) } == 0 {
        return Err(Error::last_os_error());
    }
    let name = format!(r"\\.\pipe\clrd-{session}");
    let wide: Vec<u16> = name.encode_utf16().chain([0]).collect();
    let mut last = Error::other("the running app did not answer");
    // Two seconds: the app may be busy with another start, or just gone.
    for _ in 0..40 {
        // Inbound, with the default access rules: this user may write to it,
        // other users may not. Only the first maker succeeds.
        let pipe = unsafe {
            CreateNamedPipeW(
                wide.as_ptr(),
                PIPE_ACCESS_INBOUND | FILE_FLAG_FIRST_PIPE_INSTANCE,
                PIPE_REJECT_REMOTE_CLIENTS,
                1,
                0,
                MAX,
                0,
                null(),
            )
        };
        if pipe != INVALID_HANDLE_VALUE {
            let pipe = unsafe { File::from_raw_handle(pipe) };
            std::thread::spawn(move || serve(&pipe, on_open));
            return Ok(true);
        }
        // The app may look at who writes, and no more: it cannot act as us.
        match OpenOptions::new()
            .access_mode(GENERIC_WRITE | FILE_READ_ATTRIBUTES)
            .security_qos_flags(SECURITY_IDENTIFICATION)
            .open(&name)
        {
            Ok(mut app) => {
                let (mut theirs, mut pid) = (0, 0);
                // Another user's program can make a pipe with this name
                // first. It is in another session, and gets nothing.
                if unsafe { GetNamedPipeServerSessionId(app.as_raw_handle(), &mut theirs) } == 0
                    || theirs != session
                {
                    return Err(Error::other("the pipe belongs to another session"));
                }
                // Windows lets this start bring a window to the front, but
                // not the app that is already running: hand the right over.
                if unsafe { GetNamedPipeServerProcessId(app.as_raw_handle(), &mut pid) } != 0 {
                    unsafe { AllowSetForegroundWindow(pid) };
                }
                // Half a link is worse than none.
                let link = if link.len() > MAX as usize { "" } else { link };
                app.write_all(link.as_bytes())?;
                return Ok(false);
            }
            Err(e) => last = e,
        }
        std::thread::sleep(Duration::from_millis(50));
    }
    Err(last)
}

/// Reads one later start at a time, for as long as the app runs.
// ponytail: a program of the same user that connects and never writes holds
// this thread, and later starts then become apps of their own. It could do
// worse with the user's files already; a read timeout if it ever matters.
fn serve(pipe: &File, on_open: impl Fn(String)) {
    let handle = pipe.as_raw_handle();
    loop {
        if unsafe { ConnectNamedPipe(handle, null_mut()) } == 0 {
            // A start that connected before this call is already there, and
            // may even have written and left.
            let error = unsafe { GetLastError() };
            if error != ERROR_PIPE_CONNECTED && error != ERROR_NO_DATA {
                unsafe { DisconnectNamedPipe(handle) };
                std::thread::sleep(Duration::from_secs(1));
                continue;
            }
        }
        let mut link = Vec::new();
        // Ends when the other side closes: that reads as an error or as
        // the end, and either way what came before it is the link.
        let _ = pipe.take(MAX.into()).read_to_end(&mut link);
        unsafe { DisconnectNamedPipe(handle) };
        // No text in it still means "open".
        on_open(String::from_utf8_lossy(&link).into_owned());
    }
}

#[cfg(test)]
mod tests {
    use std::sync::mpsc::channel;
    use std::time::Duration;

    /// The checks build this for Windows and do not run it. It has passed by
    /// hand under Wine, and not yet on a Windows computer.
    #[test]
    fn a_second_start_hands_its_link_to_the_first_and_leaves() {
        let (send, got) = channel();
        assert!(super::claim("", move |link| send.send(link).unwrap()).unwrap());
        let link = "clrd://link?server=https%3A%2F%2Fclrd.example.com%2Fsub&code=x1";
        for sent in ["", link] {
            assert!(!super::claim(sent, |_| panic!("the later start is not the app")).unwrap());
            assert_eq!(got.recv_timeout(Duration::from_secs(5)).unwrap(), sent);
        }
        // Too long to be a link: the window is still asked for.
        assert!(!super::claim(&"x".repeat(5000), |_| ()).unwrap());
        assert_eq!(got.recv_timeout(Duration::from_secs(5)).unwrap(), "");
    }
}
