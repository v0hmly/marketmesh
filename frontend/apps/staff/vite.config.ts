import { readFileSync } from "node:fs";
import { defineConfig } from "vite";
import vue from "@vitejs/plugin-vue";
export default defineConfig({
  plugins: [vue()],
  server: {
    https:
      process.env.STAFF_TLS_CERT && process.env.STAFF_TLS_KEY
        ? {
            cert: readFileSync(process.env.STAFF_TLS_CERT),
            key: readFileSync(process.env.STAFF_TLS_KEY),
          }
        : undefined,
    host: "127.0.0.1",
    port: 5174,
    strictPort: true,
  },
  build: { target: "es2022", sourcemap: false, manifest: true },
});
