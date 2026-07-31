import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { apiFetch } from "@/lib/api";

export type Scope = "read" | "write" | "replay" | "tunnel";

export type ApiKey = {
  id: string;
  name: string;
  // First 12 characters — the only key-derived value the server will show
  // again after creation.
  key_prefix: string;
  scopes: Scope[];
  created_at: string;
  last_used_at?: string | null;
  revoked_at?: string | null;
};

// The plaintext key comes back exactly once, on create.
export type CreatedApiKey = ApiKey & { key: string };

export const apiKeysKey = ["api-keys"] as const;

export function useApiKeys() {
  return useQuery({ queryKey: apiKeysKey, queryFn: () => apiFetch<ApiKey[]>("/api/api-keys") });
}

export function useCreateApiKey() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: { name: string; scopes: Scope[] }) =>
      apiFetch<CreatedApiKey>("/api/api-keys", { method: "POST", body: JSON.stringify(input) }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: apiKeysKey }),
  });
}

// Revocation is a soft delete: the row stays listed with revoked_at set, so the
// list doubles as an audit trail of what once existed.
export function useRevokeApiKey() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => apiFetch<void>(`/api/api-keys/${id}`, { method: "DELETE" }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: apiKeysKey }),
  });
}
