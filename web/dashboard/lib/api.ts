// Thrown for any 401. The query cache turns it into "show the login page"
// (see main.tsx), so no individual caller has to handle session expiry.
export class UnauthorizedError extends Error {
  constructor() {
    super("unauthorized");
    this.name = "UnauthorizedError";
  }
}

// The SPA is served from the same origin as the API in production, and Vite
// proxies to it in dev, so every path here is relative and the session cookie
// rides along automatically.
export async function apiFetch<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, {
    ...init,
    headers: { "Content-Type": "application/json", ...init?.headers },
  });

  if (res.status === 401) throw new UnauthorizedError();
  if (!res.ok) {
    // Every handler answers errors with {"error": "..."}.
    const body = (await res.json().catch(() => null)) as { error?: string } | null;
    throw new Error(body?.error ?? `request failed with ${res.status}`);
  }
  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}
