import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";

// In development the API runs separately on 8080; in the image Spring Boot
// serves this build from the same origin, so the frontend only ever fetches
// relative /api paths and needs no CORS at all.
export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: { "/api": "http://localhost:8080", "/actuator": "http://localhost:8080" },
  },
  build: { outDir: "dist", sourcemap: true },
  test: {
    environment: "jsdom",
    globals: true,
    setupFiles: ["src/test-setup.ts"],
  },
});
