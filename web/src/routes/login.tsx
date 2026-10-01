import { useMutation, useQuery } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { useId, useState } from "react";
import { z } from "zod";
import { isApiError } from "../api/client";
import { getCsrf, login } from "../api/endpoints";
import { providersQuery } from "../api/queries";
import { LoginPanel } from "../components/LoginPanel";
import { EddyMark } from "../components/Sidebar";
import { safeReturnTo } from "../lib/links";
import { useTitle } from "../lib/title";

export const Route = createFileRoute("/login")({
  validateSearch: z.object({
    returnTo: z.string().optional().catch(undefined),
    // ?local=1 shows the break-glass password form.
    local: z.coerce.string().optional().catch(undefined),
    // Set by the hub after a failed GitHub or OIDC callback.
    error: z
      .string()
      .regex(/^[a-z_]{1,32}$/)
      .optional()
      .catch(undefined),
  }),
  component: LoginPage,
});

function LocalForm({ target, autoFocus }: { target: string; autoFocus: boolean }) {
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
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

  return (
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
          autoFocus={autoFocus}
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
      <button type="submit" className="btn btn-primary" disabled={signIn.isPending || !username || !password}>
        {signIn.isPending ? "Signing in…" : "Sign in"}
      </button>
    </form>
  );
}

function LoginPage() {
  useTitle("sign in");
  const { returnTo, local, error } = Route.useSearch();
  const target = safeReturnTo(returnTo);
  const providers = useQuery(providersQuery);
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
        {p && (
          <LoginPanel
            providers={p}
            returnTo={target}
            showLocal={local === "1"}
            error={error}
            localForm={<LocalForm target={target} autoFocus={p.providers.length === 0 || local === "1"} />}
          />
        )}
      </div>
    </main>
  );
}
