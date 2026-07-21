import { noteOutage, noteReachable } from "../lib/outage";
import type { APIError } from "./generated";

export class HttpError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.name = "HttpError";
    this.status = status;
  }
}

/**
 * Whether a request went unanswered, as opposed to being answered badly.
 *
 * A dead network and a 5xx are the same fact wearing different clothes: nothing
 * that could have answered got to look at the request. Which one surfaces is an
 * accident of where the failure sits — a phone in a tunnel gets the TypeError
 * fetch() rejects with, the same phone behind a proxy fronting a crashed server
 * gets a 502.
 *
 * This is the line every offline fallback draws: past it, serve what's on disk.
 * A 4xx is on the near side, because it is the server answering — a 404 means
 * the comic is gone, not that we couldn't ask.
 */
export function isUnanswered(err: unknown): boolean {
  return !(err instanceof HttpError) || err.status >= 500;
}

type Json = Record<string, unknown> | unknown[] | null;

interface RequestOptions extends Omit<RequestInit, "body"> {
  body?: Json;
}

async function request<T>(path: string, opts: RequestOptions = {}): Promise<T> {
  const { body, headers, ...rest } = opts;
  const res = await fetch(path, {
    credentials: "include",
    headers: {
      Accept: "application/json",
      ...(body !== undefined ? { "Content-Type": "application/json" } : {}),
      ...headers,
    },
    body: body !== undefined ? JSON.stringify(body) : undefined,
    ...rest,
  });

  // Judged here rather than at the fallbacks, because reaching a fallback is not
  // the same as noticing: an outage that only ever hits a mutation still means
  // the server is down, and a success anywhere is enough to clear the notice.
  // A rejected fetch() never gets here, which is deliberate — see lib/outage.
  if (res.status >= 500) noteOutage();
  else noteReachable();

  if (!res.ok) {
    let message = res.statusText;
    try {
      // The server writes user-safe text into .error; show it verbatim rather
      // than inventing our own wording for a failure we don't understand.
      const data = (await res.json()) as APIError;
      if (data?.error) message = data.error;
    } catch {
      // non-JSON error body; keep statusText
    }
    throw new HttpError(res.status, message);
  }

  if (res.status === 204) return undefined as T;
  const text = await res.text();
  return (text ? JSON.parse(text) : undefined) as T;
}

export const http = {
  get: <T>(path: string, opts?: RequestOptions) =>
    request<T>(path, { ...opts, method: "GET" }),
  post: <T>(path: string, body?: Json, opts?: RequestOptions) =>
    request<T>(path, { ...opts, method: "POST", body }),
  put: <T>(path: string, body?: Json, opts?: RequestOptions) =>
    request<T>(path, { ...opts, method: "PUT", body }),
  patch: <T>(path: string, body?: Json, opts?: RequestOptions) =>
    request<T>(path, { ...opts, method: "PATCH", body }),
  del: <T>(path: string, opts?: RequestOptions) =>
    request<T>(path, { ...opts, method: "DELETE" }),
};
