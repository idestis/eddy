import { createRootRouteWithContext, Link, Outlet } from "@tanstack/react-router";
import type { RouterContext } from "../router";

export const Route = createRootRouteWithContext<RouterContext>()({
  component: Outlet,
  notFoundComponent: NotFound,
});

function NotFound() {
  return (
    <div className="login">
      <div className="login-card">
        <h1>Not found</h1>
        <p>This page does not exist, or you cannot see it.</p>
        <Link to="/" className="btn">
          Go home
        </Link>
      </div>
    </div>
  );
}
