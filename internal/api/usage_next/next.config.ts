import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  output: "export",
  basePath: "/usage",
  images: { unoptimized: true },
};

export default nextConfig;
