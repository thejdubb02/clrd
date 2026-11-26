export function serverURL(value) {
  let url;
  try {
    url = new URL(value);
  } catch {
    throw new Error('Enter a valid clrd server URL');
  }
  const local = ['localhost', '127.0.0.1', '[::1]'].includes(url.hostname);
  if ((url.protocol !== 'https:' && !(local && url.protocol === 'http:')) ||
      url.username || url.password || url.search || url.hash) {
    throw new Error('Use an HTTPS clrd URL without credentials, query or fragment. Loopback may use HTTP');
  }
  return url.href.replace(/\/+$/, '');
}

export async function request(server, path, { token, body, method, timeout = 10_000 } = {}) {
  return fetch(serverURL(server) + path, {
    method: method ?? (body === undefined ? 'GET' : 'POST'),
    body,
    headers: {
      Accept: 'application/json',
      ...(body === undefined ? {} : { 'Content-Type': 'application/json' }),
      ...(token ? { Authorization: 'Bearer ' + token } : {}),
    },
    redirect: 'error',
    signal: AbortSignal.timeout(timeout),
  });
}
