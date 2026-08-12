// Клиент бэкенда test-future. Базовый URL берётся из публичной env-переменной,
// которую прокидывает next.config.ts (по умолчанию http://localhost:8081).

import type { AssetDetail, AssetPrice, ForecastSummary, ForecastView } from "./types";

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

/** GET /api/forecasts — последние прогнозы по всем монетам. */
export async function fetchForecasts(): Promise<ForecastSummary[]> {
  const res = await fetch(`${API_URL}/api/forecasts`, {
    cache: "no-store",
  });
  if (!res.ok) {
    throw new Error(`/api/forecasts вернул ${res.status}`);
  }
  return res.json() as Promise<ForecastSummary[]>;
}

/** GET /api/forecasts/:asset — детальная карточка прогноза. */
export async function fetchForecastDetail(asset: number | string): Promise<ForecastView> {
  const res = await fetch(`${API_URL}/api/forecasts/${asset}`, {
    cache: "no-store",
  });
  if (!res.ok) {
    throw new Error(`/api/forecasts/${asset} вернул ${res.status}`);
  }
  return res.json() as Promise<ForecastView>;
}

export { API_URL };
