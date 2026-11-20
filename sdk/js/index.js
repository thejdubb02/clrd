// Tiny client for clrd (https://github.com/thejdubb02/clrd). No dependencies, uses fetch.

export class ClrdError extends Error {
  constructor(status, message, code) {
    super(`${status}: ${message}`);
    this.name = "ClrdError";
    this.status = status;
    this.code = code;
  }
}

const env = (k) => (typeof process !== "undefined" ? process.env[k] : undefined);
const e = encodeURIComponent;

export class Client {
  constructor({ url, token, fetch: f } = {}) {
    this.url = (url ?? env("CLRD_URL") ?? "").replace(/\/+$/, "");
    this.token = token ?? env("CLRD_TOKEN");
    this.fetch = f ?? ((...a) => globalThis.fetch(...a)); // unbound native fetch throws in browsers
  }

  async #req(method, path, body) {
    if (!this.url) throw new ClrdError(0, "no server URL: set CLRD_URL or pass { url }");
    const headers = { "Content-Type": "application/json" };
    if (this.token) headers.Authorization = `Bearer ${this.token}`;
    const res = await this.fetch(this.url + path, {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    const text = await res.text();
    let data = null;
    try {
      data = text ? JSON.parse(text) : null;
    } catch {
      data = null;
    }
    if (!res.ok) throw new ClrdError(res.status, data?.error ?? text, data?.code);
    return data;
  }

  /** id is your stable key (external_id). */
  send(topic, message, { id, ...opts } = {}) {
    const body = { topic, message, ...opts };
    if (id !== undefined) body.external_id = id;
    return this.#req("POST", "/api/v1/notifications", body);
  }

  /** resolve(topic, externalId) or resolve(serverId). */
  resolve(topicOrId, externalId) {
    return this.#byKey("resolve", topicOrId, externalId);
  }

  /** ack(topic, externalId) or ack(serverId). */
  ack(topicOrId, externalId) {
    return this.#byKey("ack", topicOrId, externalId);
  }

  dismiss(id) {
    return this.#req("POST", `/api/v1/notifications/${e(id)}/dismiss`);
  }

  snooze(id, duration = "1h") {
    return this.#req("POST", `/api/v1/notifications/${e(id)}/snooze`, { for: duration });
  }

  get(id) {
    return this.#req("GET", `/api/v1/notifications/${e(id)}`);
  }

  list({ topic, state = "open", limit } = {}) {
    const q = new URLSearchParams({ state });
    if (topic) q.set("topic", topic);
    if (limit) q.set("limit", String(limit));
    return this.#req("GET", `/api/v1/notifications?${q}`);
  }

  #byKey(action, topicOrId, externalId) {
    return externalId === undefined
      ? this.#req("POST", `/api/v1/notifications/${e(topicOrId)}/${action}`)
      : this.#req("POST", `/${e(topicOrId)}/${e(externalId)}/${action}`);
  }
}

let def;
const c = () => (def ??= new Client());

export const send = (...a) => c().send(...a);
export const resolve = (...a) => c().resolve(...a);
export const ack = (...a) => c().ack(...a);
export const dismiss = (...a) => c().dismiss(...a);
export const snooze = (...a) => c().snooze(...a);
export const get = (...a) => c().get(...a);
export const list = (...a) => c().list(...a);
