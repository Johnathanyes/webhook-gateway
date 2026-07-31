import { useInfiniteQuery, useQuery } from "@tanstack/react-query";

import { apiFetch } from "@/lib/api";

export type EventSummary = {
  id: string;
  source_id: string;
  content_type?: string;
  dedupe_key?: string;
  verified: boolean;
  received_at: string;
};

export type EventDetail = {
  id: string;
  source_id: string;
  raw_headers: unknown;
  // Base64: the server sends the stored bytes verbatim as a Go []byte.
  raw_body: string;
  content_type?: string;
  parsed_body?: unknown;
  dedupe_key?: string;
  verified: boolean;
  received_at: string;
};

export type TraceAttempt = {
  attempt_number: number;
  request_headers?: unknown;
  response_status_code?: number;
  response_headers?: unknown;
  response_body_truncated?: string;
  error?: string;
  duration_ms?: number;
  attempted_at: string;
};

export type TraceDelivery = {
  delivery_id: string;
  destination_id: string;
  status: string;
  attempt_count: number;
  queued_at: string;
  dead_lettered_at?: string;
  attempts: TraceAttempt[];
};

export type EventTrace = {
  event_id: string;
  received_at: string;
  verified: boolean;
  deliveries: TraceDelivery[];
};

// The set the list endpoint accepts; anything else is a 400.
export const deliveryStatuses = ["pending", "succeeded", "failed", "dead_lettered", "paused"] as const;

export type EventFilters = {
  source_id?: string;
  verified?: string;
  delivery_status?: string;
  search?: string;
  after?: string;
  before?: string;
};

type ListEventsResponse = { events: EventSummary[]; next_cursor?: string };

export function useEvents(filters: EventFilters) {
  return useInfiniteQuery({
    queryKey: ["events", filters],
    queryFn: ({ pageParam }) => {
      const params = new URLSearchParams();
      for (const [key, value] of Object.entries(filters)) {
        if (value) params.set(key, value);
      }
      if (pageParam) params.set("cursor", pageParam);
      return apiFetch<ListEventsResponse>(`/api/events?${params.toString()}`);
    },
    initialPageParam: "",
    // The server omits next_cursor on the last page.
    getNextPageParam: (lastPage) => lastPage.next_cursor || undefined,
  });
}

export function useEvent(id: string) {
  return useQuery({ queryKey: ["event", id], queryFn: () => apiFetch<EventDetail>(`/api/events/${id}`) });
}

export function useEventTrace(id: string) {
  return useQuery({
    queryKey: ["event", id, "trace"],
    queryFn: () => apiFetch<EventTrace>(`/api/events/${id}/trace`),
  });
}
