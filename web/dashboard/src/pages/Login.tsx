import { useState } from "react";
import { useLocation, useNavigate } from "react-router";

import { useLogin } from "../session";

type LocationState = { from?: string } | null;

export default function Login() {
  const [password, setPassword] = useState("");
  const login = useLogin();
  const navigate = useNavigate();
  const location = useLocation();

  // Send them back where they were headed before the guard intercepted.
  const from = (location.state as LocationState)?.from ?? "/";

  return (
    <main>
      <h1>Webhook Gateway</h1>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          login.mutate(password, { onSuccess: () => navigate(from, { replace: true }) });
        }}
      >
        <label htmlFor="password">Admin password</label>
        <input
          id="password"
          type="password"
          autoFocus
          autoComplete="current-password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
        />
        <button type="submit" disabled={login.isPending || password === ""}>
          {login.isPending ? "Signing in…" : "Sign in"}
        </button>
        {login.isError && <p role="alert">{login.error.message}</p>}
      </form>
    </main>
  );
}
