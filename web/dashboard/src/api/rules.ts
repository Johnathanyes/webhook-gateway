import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { apiFetch } from "@/lib/api";

export type RuleInput = {
  source_id: string;
  name: string;
  // CEL over body, headers, source. e.g. body.amount > 10000
  expression: string;
  action: "drop" | "route";
  // Required when action is "route"; ignored for "drop".
  route_destination_ids?: string[];
  // Lower runs first; first match wins.
  priority: number;
  enabled: boolean;
};

export type Rule = RuleInput & {
  id: string;
  created_at: string;
  updated_at: string;
};

export const rulesKey = ["rules"] as const;

export function useRules() {
  return useQuery({ queryKey: rulesKey, queryFn: () => apiFetch<Rule[]>("/api/rules") });
}

export function useCreateRule() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: RuleInput) =>
      apiFetch<Rule>("/api/rules", { method: "POST", body: JSON.stringify(input) }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: rulesKey }),
  });
}

export function useUpdateRule() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ id, input }: { id: string; input: RuleInput }) =>
      apiFetch<Rule>(`/api/rules/${id}`, { method: "PATCH", body: JSON.stringify(input) }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: rulesKey }),
  });
}

export function useDeleteRule() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => apiFetch<void>(`/api/rules/${id}`, { method: "DELETE" }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: rulesKey }),
  });
}
