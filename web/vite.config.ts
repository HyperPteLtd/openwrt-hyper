/**
 * Static Preact build. The router serves plain files from /luci-static/hyper/
 * (LuCI's view hyper/app loads index.html in an iframe), so there is no server
 * rendering and no router library: tabs are switched with the URL hash.
 */
import preact from "@preact/preset-vite";
import { defineConfig } from "vite";

export default defineConfig(({ command }) => ({
  base: command === "build" ? "/luci-static/hyper/" : "/",
  plugins: [preact()],
  build: {
    outDir: "../luci-app-hyper/htdocs/luci-static/hyper",
    emptyOutDir: true,
    target: "es2020",
    sourcemap: false,
  },
  server: {
    port: 5173,
  },
}));
