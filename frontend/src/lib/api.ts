// Клиент бэкенда test-future. Базовый URL берётся из публичной env-переменной,
// которую прокидывает next.config.ts (по умолчанию http://localhost:8081).

import type { AssetDetail, AssetPrice } from "./types";

const API_URL =
  process.env.NEXT_PUBLIC_API_URL ?? "http://localhost:8081";

/** GET /api/assets — список монет с последней ценой. */
export async function fetchAssets(): Promise<AssetPrice[]> {
  const res = await fetch(`${API_URL}/api/assets`, {
    // Для серверных компонентов: всегда свежие данные.
    cache: "no-store",
  });
  if (!res.ok) {
    throw new Error(`/api/assets вернул ${res.status}`);
  }
  return res.json() as Promise<AssetPrice[]>;
}

/** GET /api/assets/:id — детальная карточка монеты с индикаторами. */
export async function fetchAssetDetail(id: number | string): Promise<AssetDetail> {
  const res = await fetch(`${API_URL}/api/assets/${id}`, {
    cache: "no-store",
  });
  if (!res.ok) {
    throw new Error(`/api/assets/${id} вернул ${res.status}`);
  }
  return res.json() as Promise<AssetDetail>;
}

export { API_URL };
