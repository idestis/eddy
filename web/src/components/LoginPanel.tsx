import { type ReactNode, useEffect, useId, useRef, useState } from "react";
import type { Providers, SignInProvider } from "../api/types";
import { Icon } from "./Icon";

/** Messages for the hub's ?error= codes after an OAuth/OIDC callback. Deliberately generic. */
export const LOGIN_ERRORS: Record<string, string> = {
  state: "Your sign-in expired or was started in another tab. Try again.",
  denied:
    "Your account is not allowed to sign in to this hub. Ask an administrator which organizations, teams, domains or groups are allowed.",
  provider: "The sign-in provider returned an error. Try again.",
  cancelled: "Sign-in was cancelled.",
  rate_limited: "Too many sign-in attempts. Wait a minute and try again.",
  unavailable: "The sign-in provider cannot be reached right now. Try again shortly.",
  invalid_request: "That sign-in link is not valid.",
};

export function loginErrorText(code: string): string {
  return LOGIN_ERRORS[code] ?? "Sign-in failed. Try again.";
}

/** The provider's login URL with returnTo. Only hub-local /auth/ paths are followed. */
export function providerHref(p: SignInProvider, returnTo: string): string | undefined {
  if (!p.loginURL.startsWith("/auth/") || p.loginURL.startsWith("//")) return undefined;
  return returnTo && returnTo !== "/" ? `${p.loginURL}?returnTo=${encodeURIComponent(returnTo)}` : p.loginURL;
}

function Divider({ children }: { children: string }) {
  return (
    <div className="flex items-center gap-2.5 text-12 text-ink-3 before:h-px before:flex-1 before:bg-line after:h-px after:flex-1 after:bg-line">
      {children}
    </div>
  );
}

const AUTO_REDIRECT_MS = 600;

export interface LoginPanelProps {
  providers: Providers;
  /** Already checked with safeReturnTo. */
  returnTo: string;
  /** /login?local=1: show the break-glass password form. */
  showLocal: boolean;
  /** /login?error=<code> from a failed OAuth/OIDC callback. */
  error?: string;
  /** The password form, rendered when password sign-in is shown. */
  localForm: ReactNode;
  /** Where the browser goes for the single-provider auto sign-in. */
  redirect?: (url: string) => void;
  autoRedirectMs?: number;
}

/** The sign-in options: provider buttons, the password form (or its break-glass link), proxy and dev. */
export function LoginPanel({
  providers: p,
  returnTo,
  showLocal,
  error,
  localForm,
  redirect = (url) => window.location.assign(url),
  autoRedirectMs = AUTO_REDIRECT_MS,
}: LoginPanelProps) {
  const devId = useId();
  const [devUser, setDevUser] = useState("dev@example.com");
  const [stayed, setStayed] = useState(false);
  const redirectRef = useRef(redirect);
  redirectRef.current = redirect;

  const localVisible = p.local.enabled && (p.local.mode !== "breakglass" || showLocal);
  const breakglassLink = p.local.enabled && p.local.mode === "breakglass" && !showLocal;
  const providers = p.providers.filter((x) => providerHref(x, returnTo));
  const only = providers.length === 1 ? providers[0] : undefined;
  // One provider and nothing else to choose: go straight there. An error or ?local=1 stops it,
  // so a failing provider never loops.
  const auto = only && !localVisible && !p.proxy && !p.dev && !error && !showLocal && !stayed;
  const autoHref = auto && only ? providerHref(only, returnTo) : undefined;

  useEffect(() => {
    if (!autoHref) return;
    const t = setTimeout(() => redirectRef.current(autoHref), autoRedirectMs);
    return () => clearTimeout(t);
  }, [autoHref, autoRedirectMs]);

  const breakglassHref = `/login?local=1${returnTo !== "/" ? `&returnTo=${encodeURIComponent(returnTo)}` : ""}`;

  if (auto && only) {
    return (
      <>
        <p role="status">Signing you in with {only.name}…</p>
        <button type="button" className="btn" onClick={() => setStayed(true)}>
          Show other sign-in options
        </button>
        {breakglassLink && (
          <a className="self-center text-12 text-ink-3 hover:underline" href={breakglassHref}>
            Admin sign-in
          </a>
        )}
      </>
    );
  }

  const none = !localVisible && !breakglassLink && !p.proxy && !p.dev && providers.length === 0;

  return (
    <>
      {error && (
        <p className="text-12-5 text-bad" role="alert">
          {loginErrorText(error)}
        </p>
      )}
      {providers.length > 0 && (
        <div className="flex flex-col gap-2">
          {providers.map((x) => (
            <a key={x.id} className="btn" href={providerHref(x, returnTo)}>
              <Icon name={x.icon === "github" ? "github" : "key"} />
              Continue with {x.name}
            </a>
          ))}
        </div>
      )}
      {localVisible && (
        <>
          {providers.length > 0 && (
            <Divider>{p.local.mode === "breakglass" ? "admin sign-in" : "or"}</Divider>
          )}
          {localForm}
        </>
      )}
      {p.proxy && (
        <>
          {(localVisible || providers.length > 0) && <Divider>or</Divider>}
          <p>
            This hub trusts your organisation's sign-in proxy. If you got here, the proxy did not identify
            you.
          </p>
          <a className="btn" href={returnTo}>
            Continue through the proxy
          </a>
        </>
      )}
      {p.dev && (
        <>
          <Divider>development only</Divider>
          <form method="get" action="/auth/dev/login">
            <div className="field">
              <label htmlFor={devId}>Fake user</label>
              <input id={devId} name="user" value={devUser} onChange={(e) => setDevUser(e.target.value)} />
            </div>
            <input type="hidden" name="groups" value="eddy:dev" />
            <button type="submit" className="btn btn-danger">
              Dev login (never in production)
            </button>
          </form>
        </>
      )}
      {breakglassLink && (
        <a className="self-center text-12 text-ink-3 hover:underline" href={breakglassHref}>
          Admin sign-in
        </a>
      )}
      {none && <p>No sign-in method is configured on this hub.</p>}
    </>
  );
}
