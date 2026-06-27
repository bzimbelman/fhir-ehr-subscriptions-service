/** @type {import('next').NextConfig} */
const nextConfig = {
  // Standalone output keeps the Docker image small — only the files Next.js
  // actually needs to run end up in .next/standalone. The Dockerfile copies
  // that subtree into the runtime image.
  output: "standalone",
  reactStrictMode: true,
  poweredByHeader: false,
};

module.exports = nextConfig;
