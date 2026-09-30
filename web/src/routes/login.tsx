import { useMutation, useQuery } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { useId, useState } from "react";
import { z } from "zod";
import { isApiError } from "../api/client";
import { getCsrf, login } from "../api/endpoints";
import { providersQuery } from "../api/queries";
import { EddyMark } from "../components/Sidebar";
import { safeReturnTo } from "../lib/links";
import { useTitle } from "../lib/title";

export const Route = createFileRoute("/login")({
  validateSearch: z.object({ returnTo: z.string().optional().catch(undefined) }),
  component: LoginPage,
});

function Divider({ children }: { children: string }) {
  return (
    <div className="flex items-center gap-2.5 text-12 text-ink-3 before:h-px before:flex-1 before:bg-line after:h-px after:flex-1 after:bg-line">
      {children}
    </div>
  );
}

function LoginPage() {
  useTitle("sign in");
  const { returnTo } = Route.useSearch();
  const target = safeReturnTo(returnTo);
  const providers = useQuery(providersQuery);
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [devUser, setDevUser] = useState("dev@example.com");
  const userId = useId();
  const passId = useId();

  const signIn = useMutation({
    mutationFn: async () => {
      const { csrf } = await getCsrf();
      await login({ username, password, returnTo: target }, csrf);
    },
    // A full load picks up the new session cookie and a fresh /me.
    onSuccess: () => window.location.assign(target),
  });

  const errorText = signIn.error
    ? isApiError(signIn.error, "unauthorized")
      ? "Wrong username or password."
      : isApiError(signIn.error, "rate_limited")
        ? "Too many attempts. Wait a minute and try again."
        : signIn.error.message
    : null;

  const p = providers.data;

  return (
    <main className="flex h-full items-center justify-center p-4 bg-page">
      <div className="flex w-[min(400px,100%)] flex-col gap-4 rounded-window border border-line bg-surface p-7 shadow-window [&_form]:flex [&_form]:flex-col [&_form]:gap-3 [&_.btn]:justify-center [&>p]:text-13 [&>p]:text-ink-3">
        <div className="flex items-center gap-[9px] text-17 font-semibold tracking-tight [&_svg]:text-c">
          <EddyMark />
          eddy
        </div>
        <div>
          <h1 className="text-20 font-semibold tracking-tight">Sign in</h1>
          <p className="text-13 text-ink-3">
            Your Kubernetes RBAC decides what you can see and do in each cluster.
          </p>
        </div>
        {providers.isPending && <p>Loading sign-in options…</p>}
        {providers.error && <p className="text-12-5 text-bad">The hub could not be reached.</p>}
        {p?.local && (
          <form
            onSubmit={(e) => {
              e.preventDefault();
              signIn.mutate();
            }}
          >
            <div className="field">
              <label htmlFor={userId}>Username</label>
              <input
                id={userId}
                autoComplete="username"
                value={username}
                onChange={(e) => setUsername(e.target.value)}
                required
                // biome-ignore lint/a11y/noAutofocus: the only thing to do on this page
                autoFocus
              />
            </div>
            <div className="field">
              <label htmlFor={passId}>Password</label>
              <input
                id={passId}
                type="password"
                autoComplete="current-password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                required
              />
            </div>
            {errorText && (
              <p className="text-12-5 text-bad" role="alert">
                {errorText}
              </p>
            )}
            <button
              type="submit"
              className="btn btn-primary"
              disabled={signIn.isPending || !username || !password}
            >
              {signIn.isPending ? "Signing in…" : "Sign in"}
            </button>
          </form>
        )}
        {p?.proxy && (
          <>
            {p.local && <Divider>or</Divider>}
            <p>
              This hub trusts your organisation's sign-in proxy. If you got here, the proxy did not identify
              you.
            </p>
            <a className="btn" href={target}>
              Continue through the proxy
            </a>
          </>
        )}
        {p?.dev && (
          <>
            <Divider>development only</Divider>
            <form method="get" action="/auth/dev/login">
              <div className="field">
                <label htmlFor={`${userId}-dev`}>Fake user</label>
                <input
                  id={`${userId}-dev`}
                  name="user"
                  value={devUser}
                  onChange={(e) => setDevUser(e.target.value)}
                />
              </div>
              <input type="hidden" name="groups" value="eddy:dev" />
              <button type="submit" className="btn btn-danger">
                Dev login (never in production)
              </button>
            </form>
          </>
        )}
        {p && !p.local && !p.proxy && !p.dev && <p>No sign-in method is configured on this hub.</p>}
      </div>
    </main>
  );
}
