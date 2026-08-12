// Типы ответов бэкенда test-future.

/** Актив с последней ценой — ответ GET /api/assets. */
export interface AssetPrice {
  asset_id: number;
  coin_id: string;
  symbol: string;
  name: string;
  price_usd: number;
  market_cap: number;
  volume: number;
  change_24h: number; // доля, 0.0123 = +1.23%
  last_updated: string; // ISO-дата или null
}
