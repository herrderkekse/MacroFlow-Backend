import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

export default defineConfig({
  plugins: [react()],
  server: {
    // `npm run dev` proxies API calls to a locally running server started
    // with ADMIN_ADDR=127.0.0.1:8081, so the UI hot-reloads against real data.
    proxy: {
      "/api": "http://127.0.0.1:8081",
    },
  },
});
