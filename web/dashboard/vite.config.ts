import path from "node:path";
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    // The "@/..." alias shadcn/ui generates its imports against.
    alias: { "@": path.resolve(import.meta.dirname, "./src") },
  },
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
