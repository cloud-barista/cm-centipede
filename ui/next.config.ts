import type { NextConfig } from 'next';
import path from 'path';

const nextConfig: NextConfig = {
  // Produce a self-contained build for small Docker images
  output: 'standalone',
  // Pin the Turbopack root so the repository's other lockfiles are ignored
  turbopack: {
    root: path.resolve(__dirname),
  },
};

export default nextConfig;
