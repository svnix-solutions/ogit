import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

// `npm run dev` serves the app on :5173 and forwards the API and git routes
// to a server on :8080 (`./ogit server`), so cookies stay same-origin.
const backend = "http://localhost:8080";

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: { alias: { "@": "/src" } },
  server: {
    proxy: { "/api": backend, "/documentation": backend, "/api-spec.json": backend },
  },
  build: {
    outDir: "dist",
    emptyOutDir: true,
    rolldownOptions: {
      output: {
        // Libraries change less often than the app: keep them in their own
        // file so browsers keep it cached across deployments.
        advancedChunks: {
          groups: [{ name: "vendor", test: /node_modules[\\/](react|react-dom|react-router|scheduler|@tanstack)[\\/]/ }],
        },
      },
    },
  },
  test: { environment: "jsdom", globals: true, setupFiles: ["./src/test/setup.ts"] },
});
