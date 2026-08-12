import type { NextConfig } from "next";

// Next.js 16: конфиг на TypeScript. Порт берётся из FRONTEND_PORT в скриптах package.json.
const nextConfig: NextConfig = {
  // Проброс серверной переменной окружения на клиент.
  env: {
    NEXT_PUBLIC_API_URL: process.env.NEXT_PUBLIC_API_URL ?? "http://localhost:8081",
  },
};

export default nextConfig;
