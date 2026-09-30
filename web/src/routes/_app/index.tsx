import { createFileRoute, redirect } from "@tanstack/react-router";
import { clustersQuery } from "../../api/queries";

/** With one cluster, go straight to it; with several, start at the fleet overview. */
export const Route = createFileRoute("/_app/")({
  beforeLoad: async ({ context }) => {
    const clusters = await context.queryClient.ensureQueryData(clustersQuery);
    const only = clusters.length === 1 ? clusters[0] : undefined;
    if (only) throw redirect({ to: "/c/$cluster", params: { cluster: only.name } });
    throw redirect({ to: "/fleet" });
  },
});
