import type { APIError } from "./generated";

export class HttpError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.name = "HttpError";
    this.status = status;
  }
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
  del: <T>(path: string, opts?: RequestOptions) =>
    request<T>(path, { ...opts, method: "DELETE" }),
};
