import { useQuery } from "@tanstack/react-query";
import { Route, Routes } from "react-router";

// /health needs no credentials, so the shell can prove the API is reachable
// before session auth exists (#33).
function useHealth() {
  return useQuery({
    queryKey: ["health"],
    queryFn: async () => {
      const res = await fetch("/health");
      if (!res.ok) throw new Error(`health check failed: ${res.status}`);
      return res.text();
    },
  });
}

function Home() {
  const { data, error, isPending } = useHealth();

  return (
    <main>
      <h1>Webhook Gateway</h1>
      <p>Dashboard shell. Pages land in #34–#37.</p>
      <p>
        Gateway:{" "}
        {isPending ? "checking…" : error ? `unreachable (${error.message})` : `reachable (${data?.trim()})`}
      </p>
    </main>
  );
}

export default function App() {
  return (
    <Routes>
      <Route path="/" element={<Home />} />
      <Route path="*" element={<p>Not found.</p>} />
    </Routes>
  );
}
