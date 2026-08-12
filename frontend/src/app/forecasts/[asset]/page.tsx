// Детальная страница прогноза — серверный компонент (App Router).
// Тянет GET /api/forecasts/:asset с направлением, уверенностью, риском,
// декомпозицией по факторам и использованными данными. Это «прозрачный» прогноз:
// пользователь видит, из каких индикаторов и по какой формуле он получился.

import Link from "next/link";

import { fetchForecastDetail } from "@/lib/api";
import {
  formatChange,
  formatIndicator,
  formatPercent,
  formatPrice,
  formatTime,
} from "@/lib/format";
import type { ForecastFactorView } from "@/lib/types";

export const dynamic = "force-dynamic";

// Человекочитаемые названия факторов и цветов вклада.
const FACTOR_LABELS: Record<string, string> = {
  rsi: "RSI (перекупленность/перепроданность)",
  momentum: "Моментум (ROC + SMA-кроссовер)",
  volume: "Объём (интерес рынка)",
};

export default async function ForecastPage({
  params,
}: {
  params: Promise<{ asset: string }>;
}) {
  const { asset } = await params;

  let forecast: Awaited<ReturnType<typeof fetchForecastDetail>> | null = null;
  let loadError: string | null = null;

  try {
    forecast = await fetchForecastDetail(asset);
  } catch (e) {
    loadError = e instanceof Error ? e.message : String(e);
  }

  if (loadError || !forecast) {
    return (
      <main className="mx-auto max-w-3xl px-4 py-10">
        <Link
          href="/"
          className="mb-6 inline-block text-sm text-gray-400 hover:text-gray-200"
        >
          ← Назад к списку
        </Link>
        <div className="rounded-lg border border-yellow-900 bg-yellow-950/30 p-4 text-yellow-300">
          Прогноз ещё не готов ({loadError}). Worker считает прогнозы раз в час —
          после первого цикла индикаторов загляните снова.
        </div>
      </main>
    );
  }

  const isUp = forecast.direction === "up";
  const dirText = isUp ? "ВВЕРХ" : "ВНИЗ";
  const dirColor = isUp ? "text-green-400" : "text-red-400";
  const dirBg = isUp ? "bg-green-950/40 border-green-900" : "bg-red-950/40 border-red-900";
  const confidencePct = Math.round(forecast.confidence * 100);

  return (
    <main className="mx-auto max-w-3xl px-4 py-10">
      <Link
        href="/"
        className="mb-6 inline-block text-sm text-gray-400 hover:text-gray-200"
      >
        ← Назад к списку
      </Link>

      {/* Заголовок: монета + цена */}
      <header className="mb-8">
        <h1 className="text-3xl font-bold text-white">
          {forecast.symbol}
          <span className="ml-3 text-lg font-normal text-gray-400">
            {forecast.name}
          </span>
        </h1>
        <div className="mt-2 flex items-center gap-4">
          <span className="text-2xl font-semibold text-white">
            {formatPrice(forecast.data.price_usd)}
          </span>
          <span
            className={`rounded-md px-2 py-1 text-sm font-medium ${
              forecast.data.change_24h >= 0
                ? "bg-green-900/40 text-green-400"
                : "bg-red-900/40 text-red-400"
            }`}
          >
            {formatChange(forecast.data.change_24h)}
          </span>
        </div>
      </header>

      {/* Главное: направление + уверенность */}
      <section className={`mb-8 rounded-xl border p-6 ${dirBg}`}>
        <div className="text-sm uppercase tracking-wide text-gray-400">
          Прогноз на {forecast.horizon_hours}ч
        </div>
        <div className={`mt-2 flex items-center gap-3 text-5xl font-bold ${dirColor}`}>
          <span>{isUp ? "↑" : "↓"}</span>
          <span>{dirText}</span>
        </div>

        {/* Confidence прогресс-бар */}
        <div className="mt-4">
          <div className="flex items-center justify-between text-sm text-gray-400">
            <span>Уверенность</span>
            <span className="font-medium text-gray-200">{formatPercent(forecast.confidence)}</span>
          </div>
          <div className="mt-1 h-3 overflow-hidden rounded-full bg-gray-800">
            <div
              className={`h-full ${isUp ? "bg-green-500" : "bg-red-500"}`}
              style={{ width: `${confidencePct}%` }}
            />
          </div>
          <div className="mt-1 flex justify-between text-xs text-gray-600">
            <span>0.5 (никакой)</span>
            <span>1.0 (полная)</span>
          </div>
        </div>

        {/* Риск-нота */}
        <div className="mt-4 rounded-lg bg-gray-950/50 p-3">
          <div className="text-xs uppercase text-gray-500">Риск</div>
          <div className="mt-1 text-sm text-gray-300">{forecast.risk_note}</div>
        </div>
      </section>

      {/* Аргументация прогноза */}
      <section className="mb-8">
        <h2 className="mb-3 text-xl font-semibold text-white">Как считался прогноз</h2>
        <div className="rounded-lg border border-gray-800 bg-gray-900/60 p-4 text-sm leading-relaxed text-gray-300">
          {forecast.argument_text}
        </div>
        <p className="mt-2 text-xs text-gray-500">
          raw_score = {forecast.raw_score.toFixed(4)} (Σ signal × adjusted_weight).
          direction = {forecast.raw_score >= 0 ? "raw_score ≥ 0 → up" : "raw_score < 0 → down"}.
        </p>
      </section>

      {/* Декомпозиция по факторам */}
      <section className="mb-8">
        <h2 className="mb-4 text-xl font-semibold text-white">Факторы и их вклад</h2>
        <ul className="space-y-3">
          {forecast.factors.map((f) => (
            <FactorBar key={f.name} factor={f} />
          ))}
        </ul>
        <p className="mt-3 text-xs text-gray-500">
          Сигнал [-1..1]: отрицательный → вниз, положительный → вверх. Вклад = signal × adjusted_weight.
          Сумма вкладов = raw_score.
        </p>
      </section>

      {/* Использованные данные */}
      <section className="mb-8">
        <h2 className="mb-4 text-xl font-semibold text-white">Использованные данные</h2>
        <div className="grid grid-cols-2 gap-3 sm:grid-cols-3">
          <Metric label="RSI (14)" value={formatIndicator(forecast.data.rsi)} />
          <Metric label="ROC (10)" value={`${formatIndicator(forecast.data.roc)}%`} />
          <Metric label="SMA (7)" value={formatPrice(forecast.data.sma_7)} />
          <Metric label="SMA (20)" value={formatPrice(forecast.data.sma_20)} />
          <Metric label="Volume Signal" value={`${formatIndicator(forecast.data.volume_signal)}×`} />
        </div>
        <p className="mt-3 text-xs text-gray-500">
          Индикаторы рассчитаны: {formatTime(forecast.data.calculated_at)}
        </p>
      </section>

      {/* Метаданные */}
      <section className="text-xs text-gray-500">
        Прогноз посчитан: {formatTime(forecast.created_at)} · пересчёт раз в час
      </section>
    </main>
  );
}

// FactorBar — карточка фактора с визуализацией сигнала и вклада.
function FactorBar({ factor }: { factor: ForecastFactorView }) {
  const label = FACTOR_LABELS[factor.name] ?? factor.name;
  const signalPct = Math.round(Math.abs(factor.signal) * 100);
  const isPositive = factor.signal >= 0;

  return (
    <li className="rounded-xl border border-gray-800 bg-gray-900/60 p-4">
      <div className="flex items-baseline justify-between">
        <span className="text-sm font-medium text-gray-300">{label}</span>
        <span className={`text-sm font-semibold ${isPositive ? "text-green-400" : "text-red-400"}`}>
          {isPositive ? "↑" : "↓"} вклад {factor.contribution >= 0 ? "+" : ""}
          {factor.contribution.toFixed(4)}
        </span>
      </div>

      {/* Сигнал: бидирект-бар от центра */}
      <div className="mt-2">
        <div className="relative h-2 rounded-full bg-gray-800">
          <div className="absolute left-1/2 top-0 h-full w-px bg-gray-600" />
          <div
            className={`absolute top-0 h-full ${isPositive ? "bg-green-500" : "bg-red-500"}`}
            style={
              isPositive
                ? { left: "50%", width: `${signalPct / 2}%` }
                : { right: "50%", width: `${signalPct / 2}%` }
            }
          />
        </div>
        <div className="mt-1 flex justify-between text-xs text-gray-500">
          <span>сигнал {factor.signal.toFixed(3)}</span>
          <span>
            вес: base {factor.base_weight.toFixed(2)} → adjusted {factor.adjusted_weight.toFixed(3)}
          </span>
        </div>
      </div>

      <p className="mt-2 text-sm text-gray-400">{factor.detail}</p>
    </li>
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
