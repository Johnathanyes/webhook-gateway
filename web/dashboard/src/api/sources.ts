import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { apiFetch } from "@/lib/api";

export type Source = {
  id: string;
  name: string;
  provider_type: string;
  endpoint_path: string;
  created_at: string;
  dedupe_enabled: boolean;
  dedupe_strategy?: "exact" | "field";
  dedupe_field_path?: string;
  dedupe_window_seconds?: number;
};

export type CreateSourceInput = {
  name: string;
  provider_type: string;
  signing_secret?: string;
  dedupe_enabled: boolean;
  dedupe_strategy?: "exact" | "field";
  dedupe_field_path?: string;
  dedupe_window_seconds?: number;
};

export type TestEventResult = { event_id: string; verified: boolean };

export const sourcesKey = ["sources"] as const;

export function useSources() {
  return useQuery({ queryKey: sourcesKey, queryFn: () => apiFetch<Source[]>("/api/sources") });
}

export function useCreateSource() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: CreateSourceInput) =>
      apiFetch<Source>("/api/sources", { method: "POST", body: JSON.stringify(input) }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: sourcesKey }),
  });
}

// Signs the provider's sample payload with the source's real secret and runs it
// through the ingest path, so it exercises verification and routing rather than
// faking an event.
export function useSendTestEvent() {
  return useMutation({
    mutationFn: (sourceId: string) =>
      apiFetch<TestEventResult>(`/api/sources/${sourceId}/test-event`, { method: "POST" }),
  });
}
