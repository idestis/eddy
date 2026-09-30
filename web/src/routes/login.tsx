import { useMutation, useQuery } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { useId, useState } from "react";
import { z } from "zod";
import { isApiError } from "../api/client";
import { getCsrf, login } from "../api/endpoints";
import { providersQuery } from "../api/queries";
import { safeReturnTo } from "../lib/links";

export const Route = createFileRoute("/login")({
  validateSearch: z.object({ returnTo: z.string().optional().catch(undefined) }),
  component: LoginPage,
});

function LoginPage() {
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
    <main className="login">
      <div className="login-card">
        <div className="logo">
          <svg
            viewBox="0 0 24 24"
            fill="none"
            stroke="currentColor"
            strokeWidth="2.2"
            strokeLinecap="round"
            aria-hidden="true"
          >
            <path d="M12 3a9 9 0 1 0 9 9 6 6 0 0 0-6-6 4 4 0 0 0-4 4 2 2 0 0 0 2 2" />
          </svg>
          eddy
        </div>
        <div>
          <h1>Sign in</h1>
          <p>Your Kubernetes RBAC decides what you can see and do in each cluster.</p>
        </div>
        {providers.isPending && <p>Loading sign-in options…</p>}
        {providers.error && <p className="error-text">The hub could not be reached.</p>}
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
              <p className="error-text" role="alert">
                {errorText}
              </p>
            )}
            <button
              type="submit"
              className="btn primary"
              disabled={signIn.isPending || !username || !password}
            >
              {signIn.isPending ? "Signing in…" : "Sign in"}
            </button>
          </form>
        )}
        {p?.proxy && (
          <>
            {p.local && <div className="divider">or</div>}
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
            <div className="divider">development only</div>
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
              <button type="submit" className="btn danger">
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
