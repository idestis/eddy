import { createRootRouteWithContext, Link, Outlet } from "@tanstack/react-router";
import type { RouterContext } from "../router";

export const Route = createRootRouteWithContext<RouterContext>()({
  component: Outlet,
  notFoundComponent: NotFound,
});

function NotFound() {
  return (
    <div className="flex h-full items-center justify-center p-4 bg-page">
      <div className="flex w-[min(400px,100%)] flex-col gap-4 rounded-window border border-line bg-surface p-7 shadow-window">
        <h1 className="text-20 font-semibold tracking-tight">Not found</h1>
        <p className="text-13 text-ink-3">This page does not exist, or you cannot see it.</p>
        <Link to="/" className="btn self-start">
          Go home
        </Link>
      </div>
    </div>
  );
}
