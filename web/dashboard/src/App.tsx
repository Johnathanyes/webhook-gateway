import type { ReactNode } from "react";
import { Navigate, Route, Routes, useLocation } from "react-router";

import Login from "./pages/Login";
import { useLogout, useSession } from "./session";

function RequireAuth({ children }: { children: ReactNode }) {
  const { data: authenticated, isPending } = useSession();
  const location = useLocation();

  if (isPending) return null;
  if (!authenticated) return <Navigate to="/login" replace state={{ from: location.pathname }} />;
  return <>{children}</>;
}

function Home() {
  const logout = useLogout();

  return (
    <main>
      <h1>Webhook Gateway</h1>
      <p>Signed in. Config, events, and replay land in #34–#37.</p>
      <button type="button" onClick={() => logout.mutate()} disabled={logout.isPending}>
        Sign out
      </button>
    </main>
  );
}

export default function App() {
  return (
    <Routes>
      <Route path="/login" element={<Login />} />
      <Route
        path="*"
        element={
          <RequireAuth>
            <Routes>
              <Route path="/" element={<Home />} />
              <Route path="*" element={<p>Not found.</p>} />
            </Routes>
          </RequireAuth>
        }
      />
    </Routes>
  );
}
