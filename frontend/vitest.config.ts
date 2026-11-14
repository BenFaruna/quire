import { defineConfig } from "vitest/config";

// The app's own vite.config.ts loads the Wails plugin, which expects the
// generated bindings and a dev server; tests only need the DOM.
export default defineConfig({
    test: {
        environment: "jsdom",
        include: ["src/**/*.test.ts"],
    },
});
