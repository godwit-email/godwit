import { mergeConfig } from "vite";
import { defineConfig } from "vitest/config";
import viteConfig from "./vite.config.ts";

export default mergeConfig(
  viteConfig,
  defineConfig({
    // The ⌘K contract's shared cases live in webui/testdata.
    server: { fs: { allow: [".", "../webui/testdata"] } },
    test: {
      environment: "jsdom",
      globals: true,
      setupFiles: ["./src/test-setup.ts"],
      css: { include: [/frame\.css/] },
    },
  }),
);
