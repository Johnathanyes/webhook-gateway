import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { apiFetch } from "@/lib/api";

export type DestinationInput = {
  name: string;
  url: string;
  timeout_ms: number;
  max_attempts: number;
  backoff_base_seconds: number;
  backoff_max_seconds: number;
  // null means unlimited.
  rate_limit_per_second: number | null;
};

export type Destination = DestinationInput & {
  id: string;
  paused: boolean;
  created_at: string;
  updated_at: string;
};

export const destinationsKey = ["destinations"] as const;

export function useDestinations() {
  return useQuery({
    queryKey: destinationsKey,
    queryFn: () => apiFetch<Destination[]>("/api/destinations"),
  });
}

export function useCreateDestination() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: DestinationInput) =>
      apiFetch<Destination>("/api/destinations", { method: "POST", body: JSON.stringify(input) }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: destinationsKey }),
  });
}

export function useUpdateDestination() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ id, input }: { id: string; input: DestinationInput }) =>
      apiFetch<Destination>(`/api/destinations/${id}`, {
        method: "PATCH",
        body: JSON.stringify(input),
      }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: destinationsKey }),
  });
}

// Pausing snoozes queued deliveries rather than dropping them; resuming lets
// them run. Separate endpoints, not a PATCH field.
export function useSetDestinationPaused() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ id, paused }: { id: string; paused: boolean }) =>
      apiFetch<void>(`/api/destinations/${id}/${paused ? "pause" : "resume"}`, { method: "POST" }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: destinationsKey }),
  });
}

export function useDeleteDestination() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => apiFetch<void>(`/api/destinations/${id}`, { method: "DELETE" }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: destinationsKey }),
  });
}
