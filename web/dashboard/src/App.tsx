import type { ReactNode } from "react";
import { Navigate, Route, Routes, useLocation } from "react-router";

import ApiKeys from "@/pages/ApiKeys";
import Layout from "@/components/Layout";
import Destinations from "@/pages/Destinations";
import EventDetail from "@/pages/EventDetail";
import Events from "@/pages/Events";
import Login from "@/pages/Login";
import RoutesPage from "@/pages/Routes";
import Rules from "@/pages/Rules";
import Sources from "@/pages/Sources";
import { useSession } from "@/session";

function RequireAuth({ children }: { children: ReactNode }) {
  const { data: authenticated, isPending } = useSession();
  const location = useLocation();

  if (isPending) return null;
  if (!authenticated) return <Navigate to="/login" replace state={{ from: location.pathname }} />;
  return <>{children}</>;
}

export default function App() {
  return (
    <Routes>
      <Route path="/login" element={<Login />} />
      <Route
        element={
          <RequireAuth>
            <Layout />
          </RequireAuth>
        }
      >
        <Route path="/" element={<Navigate to="/events" replace />} />
        <Route path="/events" element={<Events />} />
        <Route path="/events/:id" element={<EventDetail />} />
        <Route path="/sources" element={<Sources />} />
        <Route path="/destinations" element={<Destinations />} />
        <Route path="/routes" element={<RoutesPage />} />
        <Route path="/rules" element={<Rules />} />
        <Route path="/api-keys" element={<ApiKeys />} />
        <Route path="*" element={<p className="text-sm text-muted-foreground">Not found.</p>} />
      </Route>
    </Routes>
  );
}
