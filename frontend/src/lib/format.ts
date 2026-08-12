// Форматирование цен и процентов для UI.

/** Цена в USD: $60 000.50 для дорогих монет, $0.1534 для дешёвых. */
export function formatPrice(usd: number): string {
  if (!usd) return "—";
  if (usd >= 1) {
    return new Intl.NumberFormat("en-US", {
      style: "currency",
      currency: "USD",
      maximumFractionDigits: 2,
    }).format(usd);
  }
  return new Intl.NumberFormat("en-US", {
    style: "currency",
    currency: "USD",
    minimumFractionDigits: 4,
    maximumFractionDigits: 6,
  }).format(usd);
}

/** Капитализация/объём: $1.2T, $360.0B, $15.3B, $800.0M. */
export function formatCompact(usd: number): string {
  if (!usd) return "—";
  return new Intl.NumberFormat("en-US", {
    style: "currency",
    currency: "USD",
    notation: "compact",
    maximumFractionDigits: 1,
  }).format(usd);
}

/** Изменение за 24ч в процентах: +1.23% (зелёное), -0.45% (красное). */
export function formatChange(fraction: number): string {
  if (!fraction) return "0.00%";
  const pct = fraction * 100;
  const sign = pct > 0 ? "+" : "";
  return `${sign}${pct.toFixed(2)}%`;
}

/** Время последнего обновления человекочитаемо. */
export function formatTime(iso: string | null): string {
  if (!iso) return "нет данных";
  try {
    return new Intl.DateTimeFormat("ru-RU", {
      hour: "2-digit",
      minute: "2-digit",
      second: "2-digit",
    }).format(new Date(iso));
  } catch {
    return iso;
  }
}

/** Значение индикатора с заданной точностью: «72.30» или «—» для 0/пусто. */
export function formatIndicator(value: number, decimals = 2): string {
  if (!value && value !== 0) return "—";
  return value.toFixed(decimals);
}

/** Доля [0..1] как проценты: 0.68 → «68%». Для confidence прогноза. */
export function formatPercent(fraction: number): string {
  return `${(fraction * 100).toFixed(0)}%`;
}
