import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { apiFetch } from "@/lib/api";

// A route binds one source to one destination. Without one, an ingested event
// fans out to nothing.
export type Route = {
  id: string;
  source_id: string;
  destination_id: string;
  enabled: boolean;
  created_at: string;
};

export const routesKey = ["routes"] as const;

export function useRoutes() {
  return useQuery({ queryKey: routesKey, queryFn: () => apiFetch<Route[]>("/api/routes") });
}

export function useCreateRoute() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: { source_id: string; destination_id: string }) =>
      apiFetch<Route>("/api/routes", { method: "POST", body: JSON.stringify(input) }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: routesKey }),
  });
}

export function useSetRouteEnabled() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ id, enabled }: { id: string; enabled: boolean }) =>
      apiFetch<Route>(`/api/routes/${id}`, { method: "PATCH", body: JSON.stringify({ enabled }) }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: routesKey }),
  });
}

export function useDeleteRoute() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => apiFetch<void>(`/api/routes/${id}`, { method: "DELETE" }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: routesKey }),
  });
}
