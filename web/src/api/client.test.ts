import { afterEach, describe, expect, it, vi } from "vitest";
import { type ApiError, buildUrl, CSRF_HEADER, isApiError, request, setCsrfToken } from "./client";

function mockFetch(response: Response) {
  const fn = vi.fn<typeof fetch>().mockResolvedValue(response);
  vi.stubGlobal("fetch", fn);
  return fn;
}

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });

const headersOf = (fn: ReturnType<typeof mockFetch>) => new Headers(fn.mock.calls[0]?.[1]?.headers);

afterEach(() => {
  vi.unstubAllGlobals();
  setCsrfToken("");
});

describe("request", () => {
  it("sends the CSRF header on unsafe methods only", async () => {
    setCsrfToken("tok");
    let fn = mockFetch(json({ ok: true }));
    await request("/api/v1/x", { method: "POST", body: {} });
    expect(headersOf(fn).get(CSRF_HEADER)).toBe("tok");

    fn = mockFetch(json({ ok: true }));
    await request("/api/v1/x");
    expect(headersOf(fn).get(CSRF_HEADER)).toBeNull();

    fn = mockFetch(new Response(null, { status: 204 }));
    await request("/api/v1/x", { method: "DELETE" });
    expect(headersOf(fn).get(CSRF_HEADER)).toBe("tok");
  });

  it("uses an explicit pre-session token when given", async () => {
    setCsrfToken("session");
    const fn = mockFetch(new Response(null, { status: 204 }));
    await request("/auth/local/login", { method: "POST", body: {}, csrf: "pre" });
    expect(headersOf(fn).get(CSRF_HEADER)).toBe("pre");
  });

  it("sends same-origin credentials and JSON bodies", async () => {
    const fn = mockFetch(json({}));
    await request("/api/v1/x", { method: "POST", body: { a: 1 } });
    const init = fn.mock.calls[0]?.[1];
    expect(init?.credentials).toBe("same-origin");
    expect(init?.body).toBe('{"a":1}');
    expect(headersOf(fn).get("Content-Type")).toBe("application/json");
  });

  it("returns undefined for 202 and 204", async () => {
    mockFetch(new Response(null, { status: 202 }));
    await expect(request("/x", { method: "POST" })).resolves.toBeUndefined();
  });

  it.each([
    [400, "bad_request"],
    [401, "unauthorized"],
    [403, "forbidden"],
    [404, "not_found"],
    [409, "conflict"],
    [428, "confirm_required"],
    [429, "rate_limited"],
    [500, "internal"],
  ] as const)("maps the error envelope for HTTP %i to %s", async (status, code) => {
    mockFetch(json({ error: { code, message: "nope" } }, status));
    const err = await request("/x").catch((e: unknown) => e);
    expect(isApiError(err, code)).toBe(true);
    expect((err as ApiError).status).toBe(status);
    expect((err as ApiError).message).toBe("nope");
  });

  it("distinguishes disabled from disconnected on 503 by the code", async () => {
    mockFetch(json({ error: { code: "disabled", message: "AI is off" } }, 503));
    expect(isApiError(await request("/x").catch((e: unknown) => e), "disabled")).toBe(true);
  });

  it("falls back to the status when the body is not an error envelope", async () => {
    mockFetch(new Response("<html>bad gateway</html>", { status: 503 }));
    expect(isApiError(await request("/x").catch((e: unknown) => e), "disconnected")).toBe(true);
  });

  it("ignores unknown error codes", async () => {
    mockFetch(json({ error: { code: "weird", message: "m" } }, 403));
    expect(isApiError(await request("/x").catch((e: unknown) => e), "forbidden")).toBe(true);
  });

  it("maps a failed fetch to a network error", async () => {
    vi.stubGlobal("fetch", vi.fn().mockRejectedValue(new TypeError("Failed to fetch")));
    expect(isApiError(await request("/x").catch((e: unknown) => e), "network")).toBe(true);
  });
});

describe("buildUrl", () => {
  it("drops empty and undefined values", () => {
    expect(buildUrl("/t", { a: "1", b: undefined, c: "", d: 2 })).toBe("/t?a=1&d=2");
    expect(buildUrl("/t", {})).toBe("/t");
  });
});
