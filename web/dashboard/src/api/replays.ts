import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { apiFetch } from "@/lib/api";
import type { EventFilters } from "@/api/events";

// The filter the bulk endpoint takes. Same surface as GET /api/events, but
// typed rather than stringly — verified is a real boolean here.
export type ReplayFilter = {
  source_id?: string;
  verified?: boolean;
  after?: string;
  before?: string;
  search?: string;
  delivery_status?: string;
};

export type Replay = {
  id: string;
  status: "running" | "completed" | "failed";
  filter: ReplayFilter;
  matched_count?: number;
  requeued_count?: number;
  created_at: string;
  completed_at?: string;
};

export type ReplayEventResult = { event_id: string; deliveries_created: number };

// The list page keeps filters as URL strings; the bulk endpoint wants JSON
// types. Empty values are dropped so they don't narrow the match set.
export function toReplayFilter(filters: EventFilters): ReplayFilter {
  const filter: ReplayFilter = {};
  if (filters.source_id) filter.source_id = filters.source_id;
  if (filters.verified) filter.verified = filters.verified === "true";
  if (filters.after) filter.after = filters.after;
  if (filters.before) filter.before = filters.before;
  if (filters.search) filter.search = filters.search;
  if (filters.delivery_status) filter.delivery_status = filters.delivery_status;
  return filter;
}

// Replaying one event runs it back through the normal enqueue path: new
// deliveries, new Webhook-Id, the destination's usual retry policy.
export function useReplayEvent() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (eventId: string) =>
      apiFetch<ReplayEventResult>(`/api/events/${eventId}/replay`, { method: "POST" }),
    // The deliveries are created in the request's own transaction, so the
    // trace has them by the time this resolves.
    onSuccess: (_result, eventId) => queryClient.invalidateQueries({ queryKey: ["event", eventId] }),
  });
}

export function useBulkReplay() {
  return useMutation({
    mutationFn: (filter: ReplayFilter) =>
      apiFetch<Replay>("/api/replays", { method: "POST", body: JSON.stringify(filter) }),
  });
}

// A bulk replay runs as a background job, so the only way to see counts is to
// poll the row it writes them to.
export function useReplay(id: string | null) {
  return useQuery({
    queryKey: ["replay", id],
    queryFn: () => apiFetch<Replay>(`/api/replays/${id}`),
    enabled: id !== null,
    refetchInterval: (query) => (query.state.data?.status === "running" ? 1000 : false),
  });
}
