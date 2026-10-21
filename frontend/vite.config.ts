import { fileURLToPath } from "node:url";
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import { viteSingleFile } from "vite-plugin-singlefile";

export default defineConfig({
  root: fileURLToPath(new URL(".", import.meta.url)),
  base: "./",
  plugins: [react(), tailwindcss(), viteSingleFile(), {
    name: "normalize-embedded-html",
    enforce: "post",
    generateBundle: {
      order: "post",
      handler(_options, bundle) {
        const html = bundle["index.html"];
        if (html?.type === "asset" && typeof html.source === "string")
          html.source = html.source.replace(/^[\t ]+(?=\r?$)/gm, "");
      },
    },
  }],
  resolve: { alias: { "@": fileURLToPath(new URL("./src", import.meta.url)) } },
  build: {
    outDir: process.env.CPA_UI_OUT_DIR || fileURLToPath(new URL("../internal/archive/web", import.meta.url)),
    emptyOutDir: false,
    target: "es2022",
    sourcemap: false,
    reportCompressedSize: true,
  },
  server: { host: "127.0.0.1", port: 5173, strictPort: true },
});
