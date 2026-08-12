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

/** Значение индикатора + человекочитаемая интерпретация. */
export interface IndicatorValue {
  value: number;
  interpretation: string;
}

/** Набор индикаторов в детальной карточке монеты. */
export interface IndicatorsView {
  rsi: IndicatorValue;
  roc: IndicatorValue;
  sma_7: IndicatorValue;
  sma_20: IndicatorValue;
  volume_signal: IndicatorValue;
  calculated_at: string | null; // ISO-дата или null, если ещё не посчитаны
}

/** Детальная карточка монеты — ответ GET /api/assets/:id. */
export interface AssetDetail extends AssetPrice {
  indicators: IndicatorsView;
}
