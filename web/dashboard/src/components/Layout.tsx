import { NavLink, Outlet } from "react-router";

import { Button } from "@/components/ui/button";
import { useLogout } from "@/session";
import { cn } from "@/lib/utils";

const nav = [
  { to: "/sources", label: "Sources" },
  { to: "/destinations", label: "Destinations" },
  { to: "/routes", label: "Routes" },
  { to: "/rules", label: "Rules" },
  { to: "/api-keys", label: "API keys" },
];

export default function Layout() {
  const logout = useLogout();

  return (
    <div className="flex min-h-screen">
      <aside className="flex w-56 shrink-0 flex-col border-r bg-muted/30 p-4">
        <div className="mb-6 px-2 text-sm font-semibold">Webhook Gateway</div>
        <nav className="flex flex-col gap-1">
          {nav.map((item) => (
            <NavLink
              key={item.to}
              to={item.to}
              className={({ isActive }) =>
                cn(
                  "rounded-md px-2 py-1.5 text-sm transition-colors",
                  isActive ? "bg-accent font-medium text-accent-foreground" : "text-muted-foreground hover:bg-accent/50",
                )
              }
            >
              {item.label}
            </NavLink>
          ))}
        </nav>
        <Button
          variant="ghost"
          size="sm"
          className="mt-auto justify-start text-muted-foreground"
          onClick={() => logout.mutate()}
          disabled={logout.isPending}
        >
          Sign out
        </Button>
      </aside>

      <main className="min-w-0 flex-1 p-8">
        <Outlet />
      </main>
    </div>
  );
}
