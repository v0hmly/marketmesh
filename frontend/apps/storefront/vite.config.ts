import { readFileSync } from 'node:fs';
import { defineConfig } from 'vite';
import vue from '@vitejs/plugin-vue';

// A TLS-terminating local frontdoor serves the app and API on one origin.
// Optional HTTPS makes standalone browser checks exercise secure-context APIs.
const cert = process.env.STOREFRONT_TLS_CERT;
const key = process.env.STOREFRONT_TLS_KEY;
if (Boolean(cert) !== Boolean(key)) throw new Error('Both storefront TLS files are required');
const https = cert && key ? { cert: readFileSync(cert), key: readFileSync(key) } : undefined;

export default defineConfig({
  plugins: [vue()],
  server: { https, strictPort: true },
  preview: { https, strictPort: true },
  build: { sourcemap: false, manifest: true, target: 'es2022' },
});
