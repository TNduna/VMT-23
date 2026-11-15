import { fileURLToPath } from "node:url";

// Static export: `NEXT_EXPORT=1 next build` outputs a purely static directory to web/out,
// which can be served directly from an nginx web root. Leave unset in dev (next dev)
// to keep the /api reverse proxy and hot reload.
const isExport = process.env.NEXT_EXPORT === "1";
// Vercel demo: entire site runs on mock data without a backend or /api reverse proxy.
const isMock = process.env.NEXT_PUBLIC_MOCK === "1";

/** @type {import('next').NextConfig} */
const nextConfig = {
  // Prevent a parent-directory lockfile from affecting root inference and asset path generation.
  turbopack: { root: fileURLToPath(new URL(".", import.meta.url)) },
  reactCompiler: true,
  // Allow accessing dev assets (HMR) from LAN IPs; adjust as needed.
  // In dev, allow any IPv4 origin to access /_next/* and HMR (unaffected by LAN IP changes).
  // Note: Next disallows a bare "*" for security; segmented wildcards like "*.*.*.*" match any IPv4.
  allowedDevOrigins: ["*.*.*.*"],
  compiler: {
    removeConsole: process.env.NODE_ENV === "production",
  },
  ...(isExport
    ? {
        // Pure static export: no Node runtime; unoptimized images; each route outputs <route>/index.html.
        output: "export",
        images: { unoptimized: true },
        trailingSlash: true,
      }
    : isMock
      ? {
          // Vercel mock demo: no backend, no /api reverse proxy needed.
          images: { unoptimized: true },
        }
      : {
          // Dev: proxy /api/* to the Go backend (defaults to :8787, overridable via AUTOPENTEST_API).
          async rewrites() {
            const backend = process.env.AUTOPENTEST_API ?? "http://localhost:8787";
            return [{ source: "/api/:path*", destination: `${backend}/api/:path*` }];
          },
        }),
};

export default nextConfig;
