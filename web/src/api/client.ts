// A small typed fetch wrapper for the hub API. It sends the CSRF header on
// unsafe methods and maps the error envelope of docs/api.md to ApiError.

export type ErrorCode =
  | "bad_request"
  | "unauthorized"
  | "forbidden"
  | "not_found"
  | "conflict"
  | "confirm_required"
  | "rate_limited"
  | "disconnected"
  | "disabled"
  | "internal"
  | "network";

const CODES_BY_STATUS: Record<number, ErrorCode> = {
  400: "bad_request",
  401: "unauthorized",
  403: "forbidden",
  404: "not_found",
  409: "conflict",
  428: "confirm_required",
  429: "rate_limited",
  503: "disconnected",
};

const KNOWN_CODES = new Set<string>([...Object.values(CODES_BY_STATUS), "disabled", "internal", "network"]);

export class ApiError extends Error {
  readonly status: number;
  readonly code: ErrorCode;

  constructor(status: number, code: ErrorCode, message: string) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
  }
}

export function isApiError(err: unknown, code?: ErrorCode): err is ApiError {
  return err instanceof ApiError && (code === undefined || err.code === code);
}

export const CSRF_HEADER = "X-Eddy-CSRF";

let csrfToken = "";

/** Set from GET /api/v1/me; sent on every unsafe request. */
export function setCsrfToken(token: string): void {
  csrfToken = token;
}

const SAFE_METHODS = new Set(["GET", "HEAD", "OPTIONS"]);

export interface RequestOptions {
  method?: "GET" | "POST" | "DELETE";
  body?: unknown;
  query?: Record<string, string | number | boolean | undefined>;
  signal?: AbortSignal;
  /** Overrides the session CSRF token, e.g. the pre-session token on login. */
  csrf?: string;
}

export function buildUrl(path: string, query?: RequestOptions["query"]): string {
  if (!query) return path;
  const params = new URLSearchParams();
  for (const [key, value] of Object.entries(query)) {
    if (value !== undefined && value !== "") params.set(key, String(value));
  }
  const qs = params.toString();
  return qs ? `${path}?${qs}` : path;
}

async function toApiError(res: Response): Promise<ApiError> {
  let code: ErrorCode = CODES_BY_STATUS[res.status] ?? (res.status >= 500 ? "internal" : "bad_request");
  let message = res.statusText || `HTTP ${res.status}`;
  try {
    const body: unknown = await res.json();
    if (body && typeof body === "object" && "error" in body) {
      const err = (body as { error: { code?: unknown; message?: unknown } }).error;
      if (typeof err.code === "string" && KNOWN_CODES.has(err.code)) code = err.code as ErrorCode;
      if (typeof err.message === "string" && err.message) message = err.message;
    }
  } catch {
    // Not a JSON envelope (e.g. a proxy error page); keep the status mapping.
  }
  return new ApiError(res.status, code, message);
}

export async function request<T>(path: string, opts: RequestOptions = {}): Promise<T> {
  const method = opts.method ?? "GET";
  const headers: Record<string, string> = { Accept: "application/json" };
  if (opts.body !== undefined) headers["Content-Type"] = "application/json";
  if (!SAFE_METHODS.has(method)) {
    const token = opts.csrf ?? csrfToken;
    if (token) headers[CSRF_HEADER] = token;
  }

  let res: Response;
  try {
    res = await fetch(buildUrl(path, opts.query), {
      method,
      headers,
      body: opts.body === undefined ? undefined : JSON.stringify(opts.body),
      credentials: "same-origin",
      signal: opts.signal,
    });
  } catch (err) {
    if (err instanceof DOMException && err.name === "AbortError") throw err;
    throw new ApiError(0, "network", "The hub could not be reached.");
  }

  if (!res.ok) throw await toApiError(res);
  if (res.status === 204 || res.status === 202 || res.headers.get("Content-Length") === "0") {
    return undefined as T;
  }
  const text = await res.text();
  return (text ? JSON.parse(text) : undefined) as T;
}

/** Escapes one path segment of a resource path. */
export const seg = (s: string): string => encodeURIComponent(s === "" ? "_" : s);
