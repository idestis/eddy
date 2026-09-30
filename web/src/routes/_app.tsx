import { createFileRoute, Outlet, redirect } from "@tanstack/react-router";
import { isApiError } from "../api/client";
import { clustersQuery, meQuery } from "../api/queries";
import { AppShell } from "../components/AppShell";

/** Everything behind sign-in. A 401 from /me sends the user to /login and back. */
export const Route = createFileRoute("/_app")({
  beforeLoad: async ({ context, location }) => {
    try {
      await context.queryClient.ensureQueryData(meQuery);
    } catch (err) {
      if (isApiError(err, "unauthorized"))
        throw redirect({ to: "/login", search: { returnTo: location.href } });
      throw err;
    }
  },
  loader: ({ context }) => context.queryClient.ensureQueryData(clustersQuery),
  component: () => (
    <AppShell>
      <Outlet />
    </AppShell>
  ),
  errorComponent: ({ error }) => (
    <div className="login">
      <div className="login-card" role="alert">
        <h1>Eddy could not load</h1>
        <p>{error instanceof Error ? error.message : String(error)}</p>
        <button type="button" className="btn" onClick={() => window.location.reload()}>
          Try again
        </button>
      </div>
    </div>
  ),
});
