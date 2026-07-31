import { useQuery } from "@tanstack/react-query";

import { apiFetch } from "@/lib/api";

export type Provider = {
  slug: string;
  name: string;
  description: string;
  verification_type: string;
  requires_secret: boolean;
};

// The catalog is compiled into the binary, so it only changes on upgrade.
export function useProviders() {
  return useQuery({
    queryKey: ["providers"],
    queryFn: () => apiFetch<Provider[]>("/api/providers"),
    staleTime: Infinity,
  });
}
