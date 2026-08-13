"use client";

// AssetList — клиентская компонента: показывает список монет с ценами и
// прогнозами, обновляет их по таймеру (раз в 30 секунд), чтобы UI был «живым».
// Начальные данные приходят из серверного компонента (page.tsx), дальше
// компонента дорасасывает обновления с бэкенда самостоятельно.
// Карточка монеты кликабельна — ведёт на детальную страницу прогноза.

import Link from "next/link";
import { useCallback, useEffect, useState } from "react";

import { API_URL } from "@/lib/api";
import { formatChange, formatCompact, formatPercent, formatPrice, formatTime } from "@/lib/format";
import type { AssetPrice, ForecastSummary } from "@/lib/types";

interface Props {
  initialAssets: AssetPrice[];
  initialForecasts: ForecastSummary[];
}

const REFRESH_MS = 30_000;

export default function AssetList({ initialAssets, initialForecasts }: Props) {
  const [items, setItems] = useState<AssetPrice[]>(initialAssets);
  const [forecasts, setForecasts] = useState<ForecastSummary[]>(initialForecasts);
  const [error, setError] = useState<string | null>(null);
  // updatedAt — null при SSR и первом рендере клиента, чтобы избежать hydration
  // mismatch: время на сервере и клиенте расходится на доли секунды, и React
  // падает. Реальное время ставим только после монтирования (в useEffect).
  const [updatedAt, setUpdatedAt] = useState<Date | null>(null);

  const refresh = useCallback(async () => {
    try {
      const [assetsRes, fcRes] = await Promise.all([
        fetch(`${API_URL}/api/assets`, { cache: "no-store" }),
        fetch(`${API_URL}/api/forecasts`, { cache: "no-store" }).catch(() => null),
      ]);
      if (!assetsRes.ok) throw new Error(`HTTP ${assetsRes.status}`);
      setItems((await assetsRes.json()) as AssetPrice[]);
      if (fcRes && fcRes.ok) {
        setForecasts((await fcRes.json()) as ForecastSummary[]);
      }
      setError(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setUpdatedAt(new Date());
    }
  }, []);

  useEffect(() => {
    // Сразу ставим время после монтирования (избегаем hydration mismatch),
    // затем обновляем по таймеру.
    setUpdatedAt(new Date());
    const id = setInterval(refresh, REFRESH_MS);
    return () => clearInterval(id);
  }, [refresh]);

  // map asset_id → прогноз для быстрого поиска в карточке.
  const fcByAsset = new Map(forecasts.map((f) => [f.asset_id, f]));

  return (
    <section>
      <header className="mb-6 flex items-center justify-between">
        <div>
          <h2 className="text-xl font-semibold">Отслеживаемые монеты</h2>
          <p className="text-sm text-gray-400">
            {updatedAt ? `Обновлено: ${updatedAt.toLocaleTimeString("ru-RU")}` : "Обновлено: —"}
            {error && (
              <span className="ml-2 text-red-400">ошибка: {error}</span>
            )}
          </p>
        </div>
        <button
          onClick={refresh}
          className="rounded-md border border-gray-700 px-3 py-1.5 text-sm text-gray-200 transition-colors hover:border-gray-500 hover:bg-gray-800"
        >
          Обновить
        </button>
      </header>

      <ul className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
        {items.map((a) => (
          <AssetCard key={a.coin_id} asset={a} forecast={fcByAsset.get(a.asset_id)} />
        ))}
      </ul>
    </section>
  );
}

function AssetCard({ asset, forecast }: { asset: AssetPrice; forecast?: ForecastSummary }) {
  const change = asset.change_24h;
  const up = change >= 0;
  const hasPrice = asset.price_usd > 0;

  return (
    <li>
      <Link
        href={`/forecasts/${asset.asset_id}`}
        className="block rounded-xl border border-gray-800 bg-gray-900/60 p-4 shadow-lg backdrop-blur transition-colors hover:border-gray-600 hover:bg-gray-800/60"
      >
      <div className="flex items-start justify-between">
        <div>
          <div className="text-lg font-bold text-white">
            {asset.symbol}
            <span className="ml-2 text-sm font-normal text-gray-400">
              {asset.name}
            </span>
          </div>
          <div className="mt-1 text-2xl font-semibold text-white">
            {formatPrice(asset.price_usd)}
          </div>
        </div>
        <div className="flex flex-col items-end gap-1">
          <span
            className={`rounded-md px-2 py-1 text-sm font-medium ${
              up ? "bg-green-900/40 text-green-400" : "bg-red-900/40 text-red-400"
            }`}
          >
            {formatChange(change)}
          </span>
          {forecast ? (
            <ForecastBadge direction={forecast.direction} confidence={forecast.confidence} />
          ) : null}
        </div>
      </div>

      {/* Прогноз крупно, если есть */}
      {forecast ? (
        <div className="mt-3 flex items-center gap-2 rounded-lg border border-gray-800 bg-gray-950/40 px-3 py-2">
          <span className={`text-2xl font-bold ${forecast.direction === "up" ? "text-green-400" : "text-red-400"}`}>
            {forecast.direction === "up" ? "↑" : "↓"}
          </span>
          <div className="text-sm">
            <span className="text-gray-300">
              Прогноз 24ч: <span className="font-medium">{forecast.direction === "up" ? "вверх" : "вниз"}</span>
            </span>
            <span className="ml-2 text-gray-500">
              уверенность {formatPercent(forecast.confidence)}
            </span>
          </div>
        </div>
      ) : (
        <div className="mt-3 text-xs text-gray-600">
          прогноз ещё не посчитан — загляните позже
        </div>
      )}

      <dl className="mt-4 grid grid-cols-2 gap-2 text-sm text-gray-400">
        <div>
          <dt className="text-xs uppercase text-gray-500">Капитализация</dt>
          <dd className="text-gray-200">{formatCompact(asset.market_cap)}</dd>
        </div>
        <div>
          <dt className="text-xs uppercase text-gray-500">Объём 24ч</dt>
          <dd className="text-gray-200">{formatCompact(asset.volume)}</dd>
        </div>
        <div className="col-span-2">
          <dt className="text-xs uppercase text-gray-500">Источник</dt>
          <dd className="text-gray-500">
            {hasPrice ? `CoinGecko · ${formatTime(asset.last_updated)}` : "ожидание данных…"}
          </dd>
        </div>
      </dl>
      <div className="mt-3 text-xs text-gray-500">прогноз и детали →</div>
      </Link>
    </li>
  );
}

// ForecastBadge — компактный бейдж направления прогноза для угла карточки.
function ForecastBadge({ direction, confidence }: { direction: string; confidence: number }) {
  const isUp = direction === "up";
  return (
    <span
      className={`rounded px-1.5 py-0.5 text-xs font-medium ${
        isUp ? "bg-green-950/60 text-green-500" : "bg-red-950/60 text-red-500"
      }`}
      title={`Прогноз 24ч: ${isUp ? "вверх" : "вниз"}, уверенность ${formatPercent(confidence)}`}
    >
      {isUp ? "↑" : "↓"} {formatPercent(confidence)}
    </span>
  );
}
