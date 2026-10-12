import { fileURLToPath } from "node:url";

// Static export: `NEXT_EXPORT=1 next build` emits a pure static directory to web/out, which can be dropped
// straight into the nginx web root. Dev (next dev) does not set this variable, keeping the /api reverse proxy and hot reload.
const isExport = process.env.NEXT_EXPORT === "1";
// Vercel demo: the whole site runs on mock data, no backend, no /api reverse proxy needed.
const isMock = process.env.NEXT_PUBLIC_MOCK === "1";

/** @type {import('next').NextConfig} */
const nextConfig = {
  // Avoid a parent directory's lockfile affecting root inference and asset path generation.
  turbopack: { root: fileURLToPath(new URL(".", import.meta.url)) },
  reactCompiler: true,
  // Allow access to dev assets (HMR) from LAN IPs; add or remove as needed.
  // In dev, allow any IPv4 origin to access /_next/* and HMR (unaffected by LAN IP changes).
  // Note: Next forbids a bare "*" for security, so segmented wildcards are required; "*.*.*.*" matches any IPv4.
  allowedDevOrigins: ["*.*.*.*"],
  compiler: {
    removeConsole: process.env.NODE_ENV === "production",
  },
  ...(isExport
    ? {
        // Pure static export: no Node runtime; images are not optimized; each route emits <route>/index.html.
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
          // Dev: reverse-proxy /api/* to the Go backend (defaults to :8787, overridable via AUTOPENTEST_API).
          async rewrites() {
            const backend = process.env.AUTOPENTEST_API ?? "http://localhost:8787";
            return [{ source: "/api/:path*", destination: `${backend}/api/:path*` }];
          },
        }),
};

export default nextConfig;
