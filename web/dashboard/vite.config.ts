import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
  build: {
    outDir: "dist",
    // The Go build embeds whatever is in dist/, so stale files from an older
    // build must not survive into the binary.
    emptyOutDir: true,
  },
  server: {
    proxy: {
      "/api": "http://localhost:8080",
      "/ingest": "http://localhost:8080",
      "/health": "http://localhost:8080",
      "/readyz": "http://localhost:8080",
    },
  },
});

