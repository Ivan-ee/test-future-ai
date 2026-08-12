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

// --- Прогнозы (T3) ---

/** Краткий прогноз для списка и главной — ответ GET /api/forecasts. */
export interface ForecastSummary {
  asset_id: number;
  symbol: string;
  name: string;
  direction: "up" | "down";
  confidence: number; // [0.5, 1.0]
  created_at: string; // ISO-дата
}

/** Декомпозиция вклада фактора в прогноз. */
export interface ForecastFactorView {
  name: string; // "rsi" | "momentum" | "volume" | "sentiment"
  signal: number; // [-1, 1]
  base_weight: number;
  adjusted_weight: number;
  contribution: number; // signal × adjusted_weight
  detail: string;
}

/** Использованные данные — сырые значения, из которых считались сигналы. */
export interface ForecastDataView {
  price_usd: number;
  rsi: number;
  roc: number;
  sma_7: number;
  sma_20: number;
  volume_signal: number;
  change_24h: number;
  calculated_at: string | null;
}

/** Новость с сентиментом — элемент блока «Новости» в карточке прогноза. */
export interface NewsItemView {
  title: string;
  link: string;
  published_at: string; // ISO-дата
  sentiment_score: number | null; // [-1, 1] или null, если не оценён
  sentiment_summary: string | null; // короткое резюме или null
}

/** Детальная карточка прогноза — ответ GET /api/forecasts/:asset. */
export interface ForecastView {
  asset_id: number;
  symbol: string;
  name: string;
  created_at: string;
  horizon_hours: number;
  direction: "up" | "down";
  confidence: number;
  risk_note: string;
  argument_text: string;
  raw_score: number;
  factors: ForecastFactorView[];
  data: ForecastDataView;
  news: NewsItemView[];
  // T5: результат сверки текущего прогноза (отсутствует, если ещё не resolved — <24ч).
  result?: "hit" | "miss" | "neutral";
  culprit_factor?: string;
  culprit_explanation?: string;
  // T5: история последних прогнозов по монете с результатами сверки.
  history: ForecastHistoryItem[];
}

// --- T5: точность прогнозов ---

/** Результат сверки прогноза с фактом. */
export type OutcomeResult = "hit" | "miss" | "neutral";

/** Одна строка истории прогнозов по монете с результатом сверки. */
export interface ForecastHistoryItem {
  forecast_id: number;
  created_at: string;
  direction: "up" | "down";
  confidence: number;
  result: OutcomeResult;
  culprit_factor?: string;
  culprit_explanation?: string;
  price_change_pct: number;
  actual_direction: "up" | "down";
}

/** Hit-rate одного фактора — элемент глобальной сводки точности. */
export interface FactorHitRateView {
  factor: string;
  hit_rate_ema: number;
  samples: number;
}

/** Глобальная сводка точности — ответ GET /api/accuracy. */
export interface AccuracySummary {
  total: number;
  hits: number;
  misses: number;
  neutrals: number;
  accuracy: number; // hits / (hits + misses), 0 если нет данных
  avg_confidence: number; // средняя confidence resolved-прогнозов
  per_factor: FactorHitRateView[];
}
