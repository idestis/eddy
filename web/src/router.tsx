import { MutationCache, QueryCache, QueryClient } from "@tanstack/react-query";
import { createRouter } from "@tanstack/react-router";
import { isApiError } from "./api/client";
import { routeTree } from "./routeTree.gen";

export interface RouterContext {
  queryClient: QueryClient;
}

/** Only transient failures are worth retrying; 4xx answers will not change. */
function shouldRetry(failureCount: number, err: unknown): boolean {
  if (isApiError(err)) return (err.code === "network" || err.code === "internal") && failureCount < 2;
  return failureCount < 2;
}

/** The current location as a local path, for ?returnTo=. */
export const currentPath = (): string => `${window.location.pathname}${window.location.search}`;

export function createQueryClient(): QueryClient {
  let redirecting = false;
  // A lost session anywhere sends the user to /login, then back here.
  const onError = (err: unknown) => {
    if (!isApiError(err, "unauthorized") || redirecting) return;
    if (window.location.pathname === "/login") return;
    redirecting = true;
    window.location.assign(`/login?returnTo=${encodeURIComponent(currentPath())}`);
  };
  return new QueryClient({
    queryCache: new QueryCache({ onError }),
    mutationCache: new MutationCache({ onError }),
    defaultOptions: {
      queries: { retry: shouldRetry, refetchOnWindowFocus: false },
    },
  });
}

export function createAppRouter(queryClient: QueryClient) {
  return createRouter({
    routeTree,
    context: { queryClient },
    defaultPreload: "intent",
    defaultPreloadStaleTime: 0,
    scrollRestoration: true,
  });
}

declare module "@tanstack/react-router" {
  interface Register {
    router: ReturnType<typeof createAppRouter>;
  }
}
