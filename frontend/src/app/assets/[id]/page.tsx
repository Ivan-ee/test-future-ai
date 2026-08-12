// Детальная страница монеты — серверный компонент (App Router).
// Тянет GET /api/assets/:id с текущей ценой и последними значениями
// технических индикаторов. Прогноза ещё нет (T3), но пользователь видит,
// какие данные собрали и что они значат.

import Link from "next/link";

import { fetchAssetDetail } from "@/lib/api";
import {
  formatChange,
  formatCompact,
  formatIndicator,
  formatPrice,
  formatTime,
} from "@/lib/format";

export const dynamic = "force-dynamic";

export default async function AssetDetailPage({
  params,
}: {
  params: Promise<{ id: string }>;
}) {
  const { id } = await params;

  let detail: Awaited<ReturnType<typeof fetchAssetDetail>> | null = null;
  let loadError: string | null = null;

  try {
    detail = await fetchAssetDetail(id);
  } catch (e) {
    loadError = e instanceof Error ? e.message : String(e);
  }

  if (loadError || !detail) {
    return (
      <main className="mx-auto max-w-3xl px-4 py-10">
        <Link
          href="/"
          className="mb-6 inline-block text-sm text-gray-400 hover:text-gray-200"
        >
          ← Назад к списку
        </Link>
        <div className="rounded-lg border border-red-900 bg-red-950/40 p-4 text-red-300">
          Не удалось загрузить данные монеты ({loadError}). Убедитесь, что бэкенд
          запущен (make dev).
        </div>
      </main>
    );
  }

  const change = detail.change_24h;
  const up = change >= 0;
  const ind = detail.indicators;
  const hasIndicators = ind.calculated_at !== null;

  return (
    <main className="mx-auto max-w-3xl px-4 py-10">
      <Link
        href="/"
        className="mb-6 inline-block text-sm text-gray-400 hover:text-gray-200"
      >
        ← Назад к списку
      </Link>

      <header className="mb-8">
        <h1 className="text-3xl font-bold text-white">
          {detail.symbol}
          <span className="ml-3 text-lg font-normal text-gray-400">
            {detail.name}
          </span>
        </h1>
        <div className="mt-2 flex items-center gap-4">
          <span className="text-3xl font-semibold text-white">
            {formatPrice(detail.price_usd)}
          </span>
          <span
            className={`rounded-md px-2 py-1 text-sm font-medium ${
              up
                ? "bg-green-900/40 text-green-400"
                : "bg-red-900/40 text-red-400"
            }`}
          >
            {formatChange(change)}
          </span>
        </div>
        <p className="mt-2 text-sm text-gray-500">
          Источник: CoinGecko · обновлено {formatTime(detail.last_updated)}
        </p>
        <Link
          href={`/forecasts/${detail.asset_id}`}
          className="mt-3 inline-block rounded-md border border-gray-700 px-3 py-1.5 text-sm text-gray-200 transition-colors hover:border-gray-500 hover:bg-gray-800"
        >
          → Смотреть прогноз по этой монете
        </Link>
      </header>

      <section className="mb-8 grid grid-cols-2 gap-3 sm:grid-cols-3">
        <Metric label="Капитализация" value={formatCompact(detail.market_cap)} />
        <Metric label="Объём 24ч" value={formatCompact(detail.volume)} />
      </section>

      <section>
        <h2 className="mb-4 text-xl font-semibold text-white">
          Технические индикаторы
        </h2>
        {!hasIndicators ? (
          <div className="rounded-lg border border-gray-800 bg-gray-900/60 p-4 text-gray-400">
            Индикаторы ещё не посчитаны. Worker собирает ряды цен за 30 дней —
            загляните через минуту.
          </div>
        ) : (
          <>
            <ul className="grid gap-3 sm:grid-cols-2">
              <IndicatorCard
                name="RSI (14)"
                value={formatIndicator(ind.rsi.value)}
                interpretation={ind.rsi.interpretation}
              />
              <IndicatorCard
                name="ROC (10)"
                value={formatIndicator(ind.roc.value)}
                interpretation={ind.roc.interpretation}
              />
              <IndicatorCard
                name="SMA (7)"
                value={formatPrice(ind.sma_7.value)}
                interpretation={ind.sma_7.interpretation}
              />
              <IndicatorCard
                name="SMA (20)"
                value={formatPrice(ind.sma_20.value)}
                interpretation={ind.sma_20.interpretation}
              />
              <IndicatorCard
                name="Volume Signal"
                value={`${formatIndicator(ind.volume_signal.value)}×`}
                interpretation={ind.volume_signal.interpretation}
              />
            </ul>
            <p className="mt-4 text-sm text-gray-500">
              Рассчитано: {formatTime(ind.calculated_at)}
            </p>
          </>
        )}
      </section>
    </main>
  );
}

function Metric({ label, value }: { label: string; value: string }) {
  return (
    <div className="rounded-xl border border-gray-800 bg-gray-900/60 p-4">
      <div className="text-xs uppercase text-gray-500">{label}</div>
      <div className="mt-1 text-lg text-gray-200">{value}</div>
    </div>
  );
}

function IndicatorCard({
  name,
  value,
  interpretation,
}: {
  name: string;
  value: string;
  interpretation: string;
}) {
  return (
    <li className="rounded-xl border border-gray-800 bg-gray-900/60 p-4">
      <div className="flex items-baseline justify-between">
        <span className="text-sm font-medium text-gray-300">{name}</span>
        <span className="text-xl font-semibold text-white">{value}</span>
      </div>
      <p className="mt-1 text-sm text-gray-400">{interpretation}</p>
    </li>
  );
}
