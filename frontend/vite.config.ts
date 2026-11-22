// frontend/vite.config.ts
import { defineConfig } from "vite"
import react from "@vitejs/plugin-react"

// Inside Docker, VITE_GATEWAY_URL=http://gateway:9000 (service name).
// Outside Docker (./start.sh), defaults to localhost:9000.
const gatewayTarget = process.env.VITE_GATEWAY_URL || "http://localhost:9000"

export default defineConfig({
  plugins: [react()],
  server: {
    host: true, // listen on 0.0.0.0 — required for Docker port mapping
    proxy: {
      // Routes through the Go Gateway (JWT verify + blacklist + proxy).
      // Do NOT point this directly at :8000 — that skips auth entirely.
      "/api": {
        target: gatewayTarget,
        changeOrigin: true,
      },
    },
  },
})
