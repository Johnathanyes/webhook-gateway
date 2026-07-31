import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { apiFetch, UnauthorizedError } from "../lib/api";

export const sessionKey = ["session"] as const;

// Whether the browser holds a valid session cookie. The cookie itself is
// HttpOnly, so asking the server is the only way to know.
export function useSession() {
  return useQuery({
    queryKey: sessionKey,
    queryFn: async () => {
      try {
        await apiFetch("/api/auth/session");
        return true;
      } catch (err) {
        if (err instanceof UnauthorizedError) return false;
        throw err;
      }
    },
    retry: false,
    staleTime: Infinity,
  });
}

export function useLogin() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (password: string) =>
      apiFetch("/api/auth/login", {
        method: "POST",
        body: JSON.stringify({ password }),
      }),
    onSuccess: () => queryClient.setQueryData(sessionKey, true),
  });
}

export function useLogout() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: () => apiFetch("/api/auth/logout", { method: "POST" }),
    onSuccess: () => {
      queryClient.removeQueries({
        predicate: (query) =>
          JSON.stringify(query.queryKey) !== JSON.stringify(sessionKey),
      });

      queryClient.setQueryData(sessionKey, false);
    },
  });
}
