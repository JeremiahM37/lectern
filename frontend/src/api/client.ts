export type JsonValue = string | number | boolean | null | JsonValue[] | { [key: string]: JsonValue };

export class ApiError extends Error {
  // payload is the parsed JSON error body, for endpoints that return more than
  // a detail string (reopen says when the history picker is needed).
  constructor(public readonly status: number, message: string, public readonly payload?: unknown) {
    super(message);
    this.name = 'ApiError';
  }
}

export interface ClientOptions {
  fetch?: typeof fetch;
  token?: () => string;
  onUnauthorized?: () => void;
  /** Last-known answers for the home screens (offline.ts). */
  offline?: {
    cacheable(path: string, method?: string): boolean;
    run<T>(path: string, load: () => Promise<T>): Promise<T>;
    clear(): void;
  };
  /** Skip request interceptors (a mod's own $.api calls). */
  bypassInterceptors?: boolean;
}

/**
 * Sees every request a client sends and may change or refuse it before it
 * leaves the page. Mods use it to hook messages sent to a session and
 * approval decisions in one place, whichever screen made them.
 */
export type RequestInterceptor = (
  path: string,
  options: RequestOptions,
  send: (path: string, options: RequestOptions) => Promise<unknown>,
) => Promise<unknown>;
let interceptor: RequestInterceptor | undefined;
export function setRequestInterceptor(next: RequestInterceptor | undefined) {
  interceptor = next;
}

export interface RequestOptions extends Omit<RequestInit, 'body'> {
  body?: JsonValue | FormData;
}

// Keep the same storage key so an installed app retains its authentication.
export const authToken = (): string => localStorage.getItem('lec-token') ?? '';

export function withToken(url: string, token = authToken()): string {
  if (!token) return url;
  const [path = '', hash] = url.split('#', 2);
  return `${path}${path.includes('?') ? '&' : '?'}token=${encodeURIComponent(token)}${hash === undefined ? '' : `#${hash}`}`;
}

export function createClient(options: ClientOptions = {}) {
  const request: typeof fetch = options.fetch ?? ((input, init) => globalThis.fetch(input, init));
  const getToken = options.token ?? authToken;
  const offline = options.offline;
  async function api<T>(path: string, optionsIn: RequestOptions = {}): Promise<T> {
    if (interceptor && !options.bypassInterceptors)
      return interceptor(path, optionsIn, (p, o) => direct<unknown>(p, o)) as Promise<T>;
    return direct<T>(path, optionsIn);
  }
  async function direct<T>(path: string, optionsIn: RequestOptions): Promise<T> {
    if (offline?.cacheable(path, optionsIn.method) && optionsIn.body === undefined)
      return offline.run(path, () => send<T>(path, optionsIn));
    return send<T>(path, optionsIn);
  }
  return api;
  async function send<T>(path: string, optionsIn: RequestOptions): Promise<T> {
    const { body, headers: extraHeaders, ...init } = optionsIn;
    const headers = new Headers(extraHeaders);
    const multipart = body instanceof FormData;
    if (!multipart && !headers.has('Content-Type')) headers.set('Content-Type', 'application/json');
    const token = getToken();
    if (token) headers.set('Authorization', `Bearer ${token}`);
    const response = await request(`/api${path}`, {
      ...init,
      headers,
      body: body === undefined ? undefined : multipart ? body : JSON.stringify(body),
    });
    if (!response.ok) {
      let message = response.statusText || `Request failed (${response.status})`;
      let payload: unknown;
      try {
        payload = await response.json();
        if (typeof payload === 'object' && payload !== null && 'detail' in payload && typeof payload.detail === 'string') {
          message = payload.detail;
        }
      } catch { /* Proxies can return HTML errors. Preserve the HTTP failure. */ }
      if (response.status === 401) {
        offline?.clear();
        options.onUnauthorized?.();
      }
      throw new ApiError(response.status, message, payload);
    }
    // Endpoint contracts specify null for an empty successful response.
    return (response.status === 204 ? null : await response.json()) as T;
  }
}
