import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

// During development the hub runs on https://localhost:8443 with its own CA.
const hub = process.env.NEXOS_HUB ?? "https://localhost:8443";

export default defineConfig({
  plugins: [react(), tailwindcss()],
  build: { outDir: "dist", emptyOutDir: false, sourcemap: false },
  server: {
    proxy: {
      // The hub rejects cross-origin requests; present the proxied calls as same-origin.
      "/api": { target: hub, changeOrigin: true, secure: false, ws: true, headers: { origin: hub } },
    },
  },
});
