import tailwindcss from "@tailwindcss/vite";
import { defineConfig } from "astro/config";

// Static pages only: Workers serves dist, and src/worker.ts handles /download
// and /, which it answers with /zh/ or /en/ by the browser's languages.
export default defineConfig({
	site: "https://godwit.email",
	// For `astro dev`, which runs no Worker; in production the Worker answers / first.
	redirects: { "/": "/zh-hans/" },
	vite: { plugins: [tailwindcss()] },
});
