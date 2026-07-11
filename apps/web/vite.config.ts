import { defineConfig, loadEnv } from "vite";
import react from "@vitejs/plugin-react";

declare const process: { env: Record<string, string | undefined> };

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, ".");
  const backendUrl =
    process.env.VITE_BACKEND_URL?.trim() ||
    env.VITE_BACKEND_URL?.trim() ||
    "http://127.0.0.1:8000";

  return {
    plugins: [react()],
    server: {
      port: 5173,
      proxy: {
        "/api": backendUrl,
      },
    },
  };
});
