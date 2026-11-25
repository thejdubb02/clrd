//! Linking this computer from a signed-in browser (`docs/SPEC.md` section 4,
//! "Link a phone from a signed-in browser"). The desktop app is a linked
//! device like a phone: same code, same claim, same token type.
//!
//! The browser shows a link; the user pastes it here. The app claims the
//! code with a secret only it knows and shows a 4-digit number. The browser
//! shows the same number once it sees the claim, and the user approves.

use crate::client::{agent, retry, status_error, Error};
use std::time::Duration;

/// What a pasted link says: the server and the one-time code.
#[derive(Debug, PartialEq)]
pub struct Target {
    pub base: String,
    pub code: String,
}

fn unescape(s: &str) -> Option<String> {
    let (bytes, mut out, mut i) = (s.as_bytes(), Vec::new(), 0);
    while i < bytes.len() {
        if bytes[i] == b'%' {
            out.push(u8::from_str_radix(s.get(i + 1..i + 3)?, 16).ok()?);
            i += 3;
        } else {
            out.push(bytes[i]);
            i += 1;
        }
    }
    String::from_utf8(out).ok()
}

fn param(query: &str, name: &str) -> Option<String> {
    query
        .split('&')
        .find_map(|p| p.strip_prefix(name)?.strip_prefix('='))
        .and_then(unescape)
}

/// An http(s) address with a host and nothing after the path, or `None`.
fn web_base(base: &str) -> Option<String> {
    let rest = base
        .strip_prefix("https://")
        .or_else(|| base.strip_prefix("http://"))?;
    let host = rest.split('/').next()?;
    let odd = |c: char| c.is_whitespace() || c.is_control() || "?#@\\".contains(c);
    (!host.is_empty() && !base.contains(odd)).then(|| base.trim_end_matches('/').to_string())
}

/// Reads `https://<server>/link?c=<code>` (what the browser shows) or
/// `clrd://link?server=<server>&code=<code>` (what its "Open in the clrd
/// app" link holds). `None` for anything else, a sign-in (`/pair`) link
/// included: that one signs a browser in and cannot link a device.
pub fn parse(text: &str) -> Option<Target> {
    let (head, query) = text.trim().split_once('?')?;
    let query = query.split('#').next()?;
    let (base, code) = match head.strip_prefix("clrd://link") {
        Some("" | "/") => (param(query, "server")?, param(query, "code")?),
        Some(_) => return None,
        None => (head.strip_suffix("/link")?.to_string(), param(query, "c")?),
    };
    let code_ok = !code.is_empty()
        && code
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || b == b'-' || b == b'_');
    code_ok.then_some(Target {
        base: web_base(&base)?,
        code,
    })
}

/// SHA-256 (FIPS 180-4). Written out because the check number below is its
/// only use and no crate already in the app has it.
fn sha256(data: &[u8]) -> [u8; 32] {
    const K: [u32; 64] = [
        0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4,
        0xab1c5ed5, 0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe,
        0x9bdc06a7, 0xc19bf174, 0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f,
        0x4a7484aa, 0x5cb0a9dc, 0x76f988da, 0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7,
        0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967, 0x27b70a85, 0x2e1b2138, 0x4d2c6dfc,
        0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85, 0xa2bfe8a1, 0xa81a664b,
        0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070, 0x19a4c116,
        0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
        0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7,
        0xc67178f2,
    ];
    let mut h: [u32; 8] = [
        0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a, 0x510e527f, 0x9b05688c, 0x1f83d9ab,
        0x5be0cd19,
    ];
    let mut msg = data.to_vec();
    msg.push(0x80);
    while msg.len() % 64 != 56 {
        msg.push(0);
    }
    msg.extend_from_slice(&(data.len() as u64 * 8).to_be_bytes());
    for block in msg.as_chunks::<64>().0 {
        let mut w = [0u32; 64];
        for (i, word) in block.as_chunks::<4>().0.iter().enumerate() {
            w[i] = u32::from_be_bytes(*word);
        }
        for i in 16..64 {
            let s0 = w[i - 15].rotate_right(7) ^ w[i - 15].rotate_right(18) ^ (w[i - 15] >> 3);
            let s1 = w[i - 2].rotate_right(17) ^ w[i - 2].rotate_right(19) ^ (w[i - 2] >> 10);
            w[i] = w[i - 16]
                .wrapping_add(s0)
                .wrapping_add(w[i - 7])
                .wrapping_add(s1);
        }
        let mut v = h;
        for i in 0..64 {
            let s1 = v[4].rotate_right(6) ^ v[4].rotate_right(11) ^ v[4].rotate_right(25);
            let ch = (v[4] & v[5]) ^ (!v[4] & v[6]);
            let t1 = v[7]
                .wrapping_add(s1)
                .wrapping_add(ch)
                .wrapping_add(K[i])
                .wrapping_add(w[i]);
            let s0 = v[0].rotate_right(2) ^ v[0].rotate_right(13) ^ v[0].rotate_right(22);
            let maj = (v[0] & v[1]) ^ (v[0] & v[2]) ^ (v[1] & v[2]);
            v = [
                t1.wrapping_add(s0.wrapping_add(maj)),
                v[0],
                v[1],
                v[2],
                v[3].wrapping_add(t1),
                v[4],
                v[5],
                v[6],
            ];
        }
        for (a, b) in h.iter_mut().zip(v) {
            *a = a.wrapping_add(b);
        }
    }
    let mut out = [0u8; 32];
    for (chunk, word) in out.as_chunks_mut::<4>().0.iter_mut().zip(h) {
        *chunk = word.to_be_bytes();
    }
    out
}

/// The 4-digit match number for a claim secret, the same sum the server does.
pub fn check(secret: &str) -> String {
    let h = sha256(secret.as_bytes());
    format!(
        "{:04}",
        u32::from_be_bytes([h[0], h[1], h[2], h[3]]) % 10000
    )
}

/// One poll's answer.
pub enum Claim {
    /// Not approved yet. Poll again.
    Waiting,
    /// Approved: this device's token, handed over once. Goes to the
    /// keychain and nowhere else.
    Approved(String),
    /// Denied, expired, used, or claimed by someone else. Start over.
    Gone,
}

/// A link being claimed.
pub struct Link {
    url: String,
    body: serde_json::Value,
    check: String,
    http: ureq::Agent,
}

impl Link {
    /// `name` is how this computer shows in the device list.
    pub fn start(target: &Target, name: &str) -> Result<Link, Error> {
        let mut raw = [0u8; 32];
        getrandom::fill(&mut raw).map_err(retry)?;
        let secret: String = raw.iter().map(|b| format!("{b:02x}")).collect();
        Ok(Link {
            url: format!("{}/api/v1/link/claim", target.base),
            check: check(&secret),
            body: serde_json::json!({"code": target.code, "secret": secret, "name": name}),
            http: agent(Duration::from_secs(30)),
        })
    }

    /// The number to show the user, to compare with the browser's.
    pub fn check(&self) -> &str {
        &self.check
    }

    /// Asks once. The first call binds this app's secret to the code.
    pub fn claim(&self) -> Result<Claim, Error> {
        let mut res = self
            .http
            .post(&self.url)
            .send_json(&self.body)
            .map_err(retry)?;
        match res.status().as_u16() {
            202 => Ok(Claim::Waiting),
            // 404: a server too old to link devices, or a wrong address.
            404 | 410 => Ok(Claim::Gone),
            200 => {
                #[derive(serde::Deserialize)]
                struct Approved {
                    token: String,
                }
                let a: Approved = res.body_mut().read_json().map_err(retry)?;
                Ok(Claim::Approved(a.token))
            }
            code => Err(status_error(code, None)),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn sha256_matches_the_published_vectors() {
        let hex = |d: &[u8]| -> String { sha256(d).iter().map(|b| format!("{b:02x}")).collect() };
        assert_eq!(
            hex(b""),
            "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
        );
        assert_eq!(
            hex(b"abc"),
            "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
        );
        // Two blocks, and a length that needs the padding to spill over.
        assert_eq!(
            hex(b"abcdbcdecdefdefgefghfghighijhijkijkljklmklmnlmnomnopnopq"),
            "248d6a61d20638b8e5c026930c3e6039a33ce45964ff2167f6ecedd419db06c1"
        );
    }

    #[test]
    fn check_is_four_digits_from_the_first_four_bytes() {
        // sha256("abc") starts ba7816bf = 3128432319.
        assert_eq!(check("abc"), "2319");
    }

    #[test]
    fn parse_reads_both_link_forms_and_nothing_else() {
        let t = |base: &str, code: &str| {
            Some(Target {
                base: base.into(),
                code: code.into(),
            })
        };
        assert_eq!(
            parse(" https://clrd.example.com/link?c=Ab-_9 \n"),
            t("https://clrd.example.com", "Ab-_9")
        );
        assert_eq!(
            parse("http://10.0.0.2:8080/clrd/link?c=x1"),
            t("http://10.0.0.2:8080/clrd", "x1")
        );
        assert_eq!(
            parse("clrd://link?server=https%3A%2F%2Fclrd.example.com%2Fsub&code=x1"),
            t("https://clrd.example.com/sub", "x1")
        );
        for bad in [
            "",
            "https://clrd.example.com/link",
            "https://clrd.example.com/link?c=",
            "https://clrd.example.com/pair?c=x1",
            "clrd://pair?server=https%3A%2F%2Fclrd.example.com&code=x1",
            "clrd://linker?server=https%3A%2F%2Fclrd.example.com&code=x1",
            "clrd://link?server=ftp%3A%2F%2Fclrd.example.com&code=x1",
            "clrd://link?server=https%3A%2F%2Fuser%40evil.example%2F&code=x1",
            "clrd://link?server=https%3A%2F%2F&code=x1",
            "clrd://link?server=https%3A%2F%2Fa.example%3Fq&code=x1",
            "https://clrd.example.com/link?c=a/b",
            "https://clrd.example.com/link?c=a%2",
            "javascript:alert(1)//link?c=x1",
        ] {
            assert_eq!(parse(bad), None, "{bad}");
        }
    }
}
