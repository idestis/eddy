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
    <div className="flex h-full items-center justify-center p-4 bg-page">
      <div
        className="flex w-[min(400px,100%)] flex-col gap-4 rounded-window border border-line bg-surface p-7 shadow-window"
        role="alert"
      >
        <h1 className="text-20 font-semibold tracking-tight">Eddy could not load</h1>
        <p className="text-13 text-ink-3">{error instanceof Error ? error.message : String(error)}</p>
        <button type="button" className="btn self-start" onClick={() => window.location.reload()}>
          Try again
        </button>
      </div>
    </div>
  ),
});
