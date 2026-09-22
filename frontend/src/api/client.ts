export type JsonValue = string | number | boolean | null | JsonValue[] | { [key: string]: JsonValue };

export class ApiError extends Error {
  constructor(public readonly status: number, message: string) {
    super(message);
    this.name = 'ApiError';
  }
}

export interface ClientOptions {
  fetch?: typeof fetch;
  token?: () => string;
  onUnauthorized?: () => void;
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
  return async function api<T>(path: string, optionsIn: RequestOptions = {}): Promise<T> {
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
      try {
        const payload: unknown = await response.json();
        if (typeof payload === 'object' && payload !== null && 'detail' in payload && typeof payload.detail === 'string') {
          message = payload.detail;
        }
      } catch { /* Proxies can return HTML errors. Preserve the HTTP failure. */ }
      if (response.status === 401) options.onUnauthorized?.();
      throw new ApiError(response.status, message);
    }
    // Endpoint contracts specify null for an empty successful response.
    return (response.status === 204 ? null : await response.json()) as T;
  };
}
