import { fileURLToPath } from "node:url";
import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig, type Plugin } from "vite";

// Pages make no remote request (spec §A5). Built HTML only, so dev-server HMR keeps working.
const csp =
  "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; connect-src 'self'";
const cspMeta: Plugin = {
  name: "godwit-csp",
  apply: "build",
  transformIndexHtml: () => [
    {
      tag: "meta",
      attrs: { "http-equiv": "Content-Security-Policy", content: csp },
      injectTo: "head-prepend",
    },
  ],
};

export default defineConfig({
  plugins: [react(), tailwindcss(), cspMeta],
  resolve: { alias: { "@": fileURLToPath(new URL("src", import.meta.url)) } },
  build: {
    outDir: "dist",
    emptyOutDir: true,
    rollupOptions: { input: { compose: fileURLToPath(new URL("compose.html", import.meta.url)) } },
  },
});
