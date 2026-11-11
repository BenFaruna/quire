import { defineConfig } from "vite";
import wails from "@wailsio/runtime/plugins/vite";

// https://vitejs.dev/config/
export default defineConfig({
  server: {
    host: "127.0.0.1",
    port: Number(process.env.WAILS_VITE_PORT) || 9245,
    strictPort: true,
  },
  plugins: [wails("./bindings")],
  build: {
    // Mermaid ships its layout engines as separate modules - elk is ~1.4 MB and
    // the shared diagram chunk ~600 kB - and they are fetched only when a note
    // contains a diagram. index.html loads just the entry bundle (~95 kB), so
    // the default 500 kB gate can only ever fire for those lazy vendor chunks.
    // Keep the gate just above the largest of them, so a real regression in the
    // eagerly loaded bundle still trips it.
    chunkSizeWarningLimit: 1600,
  },
});
