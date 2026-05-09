import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  reactStrictMode: true,
  // Allow reading from ../docs and ../upstream-readings during build.
  // Next.js's default file-system tracing covers app/, lib/, components/,
  // so explicit allowance isn't required for reads — but if we ever add
  // an MDX loader it'll need outputFileTracingIncludes here.
};

export default nextConfig;
