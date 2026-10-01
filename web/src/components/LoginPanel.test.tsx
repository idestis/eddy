import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Providers, SignInProvider } from "../api/types";
import { LoginPanel, type LoginPanelProps, loginErrorText, providerHref } from "./LoginPanel";

const github: SignInProvider = {
  id: "github",
  name: "GitHub",
  kind: "github",
  icon: "github",
  loginURL: "/auth/github/login",
};
const okta: SignInProvider = { id: "okta", name: "Okta", kind: "oidc", loginURL: "/auth/okta/login" };

const providers = (over: Partial<Providers> = {}): Providers => ({
  local: { enabled: false, mode: "normal" },
  proxy: false,
  dev: false,
  providers: [],
  ...over,
});

const setup = (props: Partial<LoginPanelProps> & { providers: Providers }) => {
  const redirect = vi.fn();
  render(
    <LoginPanel
      returnTo="/"
      showLocal={false}
      localForm={<form aria-label="Password sign-in" />}
      redirect={redirect}
      {...props}
    />,
  );
  return { redirect };
};

describe("LoginPanel", () => {
  beforeEach(() => vi.useFakeTimers({ shouldAdvanceTime: true }));
  afterEach(() => vi.useRealTimers());

  it("shows a button per provider with returnTo, next to the password form", () => {
    const { redirect } = setup({
      providers: providers({ local: { enabled: true, mode: "normal" }, providers: [github, okta] }),
      returnTo: "/c/prod?tab=yaml",
    });
    const gh = screen.getByRole("link", { name: "Continue with GitHub" });
    expect(gh).toHaveAttribute("href", "/auth/github/login?returnTo=%2Fc%2Fprod%3Ftab%3Dyaml");
    expect(gh.querySelector("svg")).not.toBeNull();
    expect(screen.getByRole("link", { name: "Continue with Okta" })).toHaveAttribute(
      "href",
      "/auth/okta/login?returnTo=%2Fc%2Fprod%3Ftab%3Dyaml",
    );
    expect(screen.getByRole("form", { name: "Password sign-in" })).toBeInTheDocument();
    act(() => vi.advanceTimersByTime(2000));
    expect(redirect).not.toHaveBeenCalled();
  });

  it("signs in straight away with the only provider when passwords are break-glass", () => {
    const { redirect } = setup({
      providers: providers({ local: { enabled: true, mode: "breakglass" }, providers: [github] }),
      returnTo: "/threads",
    });
    expect(screen.getByRole("status")).toHaveTextContent("Signing you in with GitHub…");
    expect(screen.queryByRole("form", { name: "Password sign-in" })).toBeNull();
    expect(screen.getByRole("link", { name: "Admin sign-in" })).toHaveAttribute(
      "href",
      "/login?local=1&returnTo=%2Fthreads",
    );
    act(() => vi.advanceTimersByTime(1000));
    expect(redirect).toHaveBeenCalledWith("/auth/github/login?returnTo=%2Fthreads");
  });

  it("lets the user stop the auto sign-in", async () => {
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    const { redirect } = setup({ providers: providers({ providers: [okta] }) });
    await user.click(screen.getByRole("button", { name: "Show other sign-in options" }));
    act(() => vi.advanceTimersByTime(2000));
    expect(redirect).not.toHaveBeenCalled();
    expect(screen.getByRole("link", { name: "Continue with Okta" })).toHaveAttribute(
      "href",
      "/auth/okta/login",
    );
  });

  it("does not auto sign-in after an error, and shows the error", () => {
    const { redirect } = setup({ providers: providers({ providers: [github] }), error: "denied" });
    expect(screen.getByRole("alert")).toHaveTextContent(/not allowed to sign in/);
    act(() => vi.advanceTimersByTime(2000));
    expect(redirect).not.toHaveBeenCalled();
  });

  it("hides the password form in break-glass mode until ?local=1", () => {
    const p = providers({ local: { enabled: true, mode: "breakglass" }, providers: [github, okta] });
    setup({ providers: p });
    expect(screen.queryByRole("form", { name: "Password sign-in" })).toBeNull();
    expect(screen.getByRole("link", { name: "Admin sign-in" })).toHaveAttribute("href", "/login?local=1");
  });

  it("shows the break-glass form at ?local=1 without redirecting", () => {
    const { redirect } = setup({
      providers: providers({ local: { enabled: true, mode: "breakglass" }, providers: [github] }),
      showLocal: true,
    });
    expect(screen.getByRole("form", { name: "Password sign-in" })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Admin sign-in" })).toBeNull();
    expect(screen.getByText("admin sign-in")).toBeInTheDocument();
    act(() => vi.advanceTimersByTime(2000));
    expect(redirect).not.toHaveBeenCalled();
  });

  it("never links to a provider URL outside /auth/", () => {
    const evil: SignInProvider = {
      id: "x",
      name: "Evil",
      kind: "oidc",
      loginURL: "https://evil.example/login",
    };
    expect(providerHref(evil, "/")).toBeUndefined();
    expect(providerHref({ ...evil, loginURL: "//evil.example/auth/" }, "/")).toBeUndefined();
    setup({ providers: providers({ providers: [evil] }) });
    expect(screen.queryByRole("link", { name: /Evil/ })).toBeNull();
    expect(screen.getByText("No sign-in method is configured on this hub.")).toBeInTheDocument();
  });

  it("maps error codes to generic messages", () => {
    expect(loginErrorText("state")).toMatch(/expired/);
    expect(loginErrorText("rate_limited")).toMatch(/Too many/);
    expect(loginErrorText("something-new")).toBe("Sign-in failed. Try again.");
  });
});
