import "react";

declare module "react" {
  // Allow CSS custom properties in style props, e.g. { "--cc": "#6D28D9" }.
  interface CSSProperties {
    [key: `--${string}`]: string | number | undefined;
  }
}

declare global {
  interface ImportMetaEnv {
    /** "1" serves the in-app mock hub (src/mock) instead of calling /api. */
    readonly VITE_MOCK?: string;
    /** Extra generated pods in the mock "dev" cluster, for list performance checks. */
    readonly VITE_MOCK_ROWS?: string;
  }
}
