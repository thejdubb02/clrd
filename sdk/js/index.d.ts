export type State = "active" | "acknowledged" | "snoozed" | "dismissed" | "resolved";

export type SendAction = { label: string } & ({ url: string } | { copy: string });

export interface Attachment {
  url: string;
  name?: string;
}

export interface Notification {
  id: string;
  topic: string;
  external_id?: string;
  title?: string;
  message: string;
  priority: number;
  tags?: string[];
  click?: string;
  icon?: string;
  attachment?: (Attachment & { type?: string; size?: number }) | null;
  actions?: { label: string; url: string; copy?: string }[];
  state: State;
  state_by?: string;
  snooze_until?: string | null;
  /** Who saw or actioned it. A mark never clears it. */
  marks?: Mark[];
  created_at: string;
  updated_at: string;
  seq: number;
}

export interface Mark {
  by: string;
  by_id: string;
  mark: "seen" | "actioned";
  note?: string;
  link?: string;
  at: string;
}

export interface MarkOptions {
  note?: string;
  link?: string;
}

export interface SendOptions {
  /** Stable key (external_id). Sending again updates the open one; resolve(topic, id) clears it. */
  id?: string;
  title?: string;
  priority?: 1 | 2 | 3 | 4 | 5;
  tags?: string[];
  click?: string;
  icon?: string;
  attachment?: Attachment;
  actions?: SendAction[];
  /** Make an existing acknowledged notification active when updating its stable key. */
  reopen?: boolean;
}

export interface ListOptions {
  topic?: string;
  state?: "open" | "all" | State;
  limit?: number;
  /** Include readable topics even if the owner is unsubscribed. */
  subscribed?: "all";
}

export interface EventCursor {
  seq: number;
  epoch: string;
}

export interface NotificationEvent {
  seq: number;
  type: string;
  notification?: Notification;
  via?: string;
}

export declare class ClrdError extends Error {
  status: number;
  code?: string;
}

export declare class Client {
  constructor(opts?: { url?: string; token?: string; fetch?: typeof fetch });
  url: string;
  send(topic: string, message: string, opts?: SendOptions): Promise<Notification>;
  resolve(topic: string, externalId: string): Promise<Notification>;
  resolve(id: string): Promise<Notification>;
  ack(topic: string, externalId: string): Promise<Notification>;
  ack(id: string): Promise<Notification>;
  dismiss(id: string): Promise<Notification>;
  snooze(id: string, duration?: string): Promise<Notification>;
  mark(id: string, mark: "seen" | "actioned", opts?: MarkOptions): Promise<Notification>;
  unmark(id: string): Promise<Notification>;
  get(id: string): Promise<Notification>;
  list(opts?: ListOptions): Promise<unknown>;
  pollEvents(cursor?: EventCursor, opts?: { topic?: string }): Promise<{
    events: NotificationEvent[];
    cursor: EventCursor;
    reset: boolean;
  }>;
}

export declare function send(topic: string, message: string, opts?: SendOptions): Promise<Notification>;
export declare function resolve(topic: string, externalId: string): Promise<Notification>;
export declare function resolve(id: string): Promise<Notification>;
export declare function ack(topic: string, externalId: string): Promise<Notification>;
export declare function ack(id: string): Promise<Notification>;
export declare function dismiss(id: string): Promise<Notification>;
export declare function snooze(id: string, duration?: string): Promise<Notification>;
export declare function mark(id: string, mark: "seen" | "actioned", opts?: MarkOptions): Promise<Notification>;
export declare function unmark(id: string): Promise<Notification>;
export declare function get(id: string): Promise<Notification>;
export declare function list(opts?: ListOptions): Promise<unknown>;
