import type { App } from "vue";

declare global {
  interface Window {
    __app?: { app: App };
  }
}

export {};
