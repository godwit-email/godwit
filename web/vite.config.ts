import { fileURLToPath } from "node:url";
import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig, type Plugin } from "vite";

// Pages make no remote request (ADR 0013): the built HTML carries this CSP. godwit-img: is Go's
// image proxy, which serves a mail body's remote images.
const csp =
  "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob: godwit-img:; connect-src 'self'";
// The dev page gets one too, loosened only for what the dev server needs: plugin-react's inline
// refresh preamble and Vite's HMR socket on localhost. Defence in depth for pasted HTML, which
// DOMParser parses into a document without a browsing context (no subresource fetch per the HTML
// spec); that WebKit makes zero requests on paste is checked in the manual e2e.
const devCsp =
  "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob: godwit-img:; connect-src 'self' ws://localhost:* http://localhost:*";
// The About page's bird decodes its meshopt-compressed model with WebAssembly and fetches the
// model's textures from blob: URLs.
const bird = (content: string, path: string) =>
  path.endsWith("/about.html")
    ? content
        .replace("script-src 'self'", "script-src 'self' 'wasm-unsafe-eval'")
        .replace("connect-src 'self'", "connect-src 'self' blob:")
    : content;
const cspMeta = (content: string, apply: Plugin["apply"]): Plugin => ({
  name: `godwit-csp-${apply}`,
  apply,
  transformIndexHtml: (_, ctx) => [
    {
      tag: "meta",
      attrs: { "http-equiv": "Content-Security-Policy", content: bird(content, ctx.path) },
      injectTo: "head-prepend",
    },
  ],
});

export default defineConfig({
  plugins: [cspMeta(csp, "build"), cspMeta(devCsp, "serve"), react(), tailwindcss()],
  server: { fs: { allow: [".", "../site/public/godwit.glb"] } }, // the About page's bird is the site's
  resolve: { alias: { "@": fileURLToPath(new URL("src", import.meta.url)) } },
  build: {
    outDir: "dist",
    emptyOutDir: true,
    rollupOptions: {
      input: {
        compose: fileURLToPath(new URL("compose.html", import.meta.url)),
        wit: fileURLToPath(new URL("wit.html", import.meta.url)),
        reader: fileURLToPath(new URL("reader.html", import.meta.url)),
        signature: fileURLToPath(new URL("signature.html", import.meta.url)),
        about: fileURLToPath(new URL("about.html", import.meta.url)),
      },
    },
  },
});
