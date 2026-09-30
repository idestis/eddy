import { createFileRoute, notFound, Outlet } from "@tanstack/react-router";
import { clustersQuery } from "../../../../api/queries";

export const Route = createFileRoute("/_app/c/$cluster")({
  loader: async ({ context, params }) => {
    const clusters = await context.queryClient.ensureQueryData(clustersQuery);
    if (!clusters.some((c) => c.name === params.cluster)) throw notFound();
  },
  component: Outlet,
});
