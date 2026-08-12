// AccuracyWidget — виджет сводки точности для главной страницы.
// Показывает фактическую точность vs заявленную уверенность и per-factor hit-rate.
// Серверный компонент: данные приходят из page.tsx через props.

import { formatPercent } from "@/lib/format";
import type { AccuracySummary } from "@/lib/types";

// Человекочитаемые названия факторов.
const FACTOR_LABELS: Record<string, string> = {
  rsi: "RSI",
  momentum: "Моментум",
  volume: "Объём",
  sentiment: "Сентимент",
};

export default function AccuracyWidget({ summary }: { summary: AccuracySummary | null }) {
  // Если данных ещё нет (нет resolved-прогнозов) — показываем заглушку.
  if (!summary || summary.total === 0) {
    return (
      <section className="mb-8 rounded-xl border border-gray-800 bg-gray-900/60 p-5">
        <h2 className="text-lg font-semibold text-white">Точность прогнозов</h2>
        <p className="mt-2 text-sm text-gray-500">
          Пока нет сверкнутых прогнозов. Прогнозы сверяются с фактом через 24ч —
          статистика точности появится после первого цикла resolve.
        </p>
      </section>
    );
  }

  const accuracyPct = Math.round(summary.accuracy * 100);
  const confidencePct = Math.round(summary.avg_confidence * 100);
  const diff = accuracyPct - confidencePct;

  return (
    <section className="mb-8 rounded-xl border border-gray-800 bg-gray-900/60 p-5">
      <h2 className="text-lg font-semibold text-white">Точность прогнозов</h2>
      <p className="mt-1 text-xs text-gray-500">
        За последние {summary.total} сверкнутых прогнозов
      </p>

      {/* Главная пара: фактическая точность vs заявленная уверенность */}
      <div className="mt-4 grid grid-cols-2 gap-4">
        <div className="rounded-lg border border-gray-800 bg-gray-950/40 p-3">
          <div className="text-xs uppercase text-gray-500">Фактическая точность</div>
          <div className="mt-1 text-3xl font-bold text-white">{accuracyPct}%</div>
          <div className="mt-1 text-xs text-gray-500">
            {summary.hits} попаданий / {summary.misses} промахов
            {summary.neutrals > 0 && ` / ${summary.neutrals} нейтрально`}
          </div>
        </div>
        <div className="rounded-lg border border-gray-800 bg-gray-950/40 p-3">
          <div className="text-xs uppercase text-gray-500">Заявленная уверенность</div>
          <div className="mt-1 text-3xl font-bold text-gray-300">{confidencePct}%</div>
          <div className="mt-1 text-xs text-gray-500">
            {diff > 0 ? (
              <span className="text-green-400">+{diff}% к заявленной</span>
            ) : diff < 0 ? (
              <span className="text-red-400">{diff}% к заявленной</span>
            ) : (
              <span className="text-gray-500">совпадает с фактом</span>
            )}
          </div>
        </div>
      </div>

      {/* Per-factor hit-rate */}
      {summary.per_factor.length > 0 && (
        <div className="mt-4">
          <div className="text-xs uppercase text-gray-500">Точность по факторам</div>
          <ul className="mt-2 space-y-2">
            {summary.per_factor.map((f) => (
              <FactorBar key={f.factor} factor={f} />
            ))}
          </ul>
        </div>
      )}
    </section>
  );
}

// FactorBar — строка точности фактора с прогресс-баром.
function FactorBar({ factor }: { factor: { factor: string; hit_rate_ema: number; samples: number } }) {
  const label = FACTOR_LABELS[factor.factor] ?? factor.factor;
  const pct = Math.round(factor.hit_rate_ema * 100);
  // Цвет: >60% зелёный, <40% красный, иначе серый.
  const barColor =
    factor.hit_rate_ema > 0.6
      ? "bg-green-500"
      : factor.hit_rate_ema < 0.4
        ? "bg-red-500"
        : "bg-gray-500";

  return (
    <li>
      <div className="flex items-baseline justify-between text-sm">
        <span className="text-gray-300">{label}</span>
        <span className="text-xs text-gray-500">
          {pct}% · {factor.samples} замеров
        </span>
      </div>
      <div className="mt-1 h-2 overflow-hidden rounded-full bg-gray-800">
        <div className={`h-full ${barColor}`} style={{ width: `${pct}%` }} />
      </div>
    </li>
  );
}
