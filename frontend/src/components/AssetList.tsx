"use client";

// AssetList — клиентская компонента: показывает список монет с ценами и
// обновляет их по таймеру (раз в 30 секунд), чтобы UI был «живым».
// Начальные данные приходят из серверного компонента (page.tsx), дальше
// компонента дорасасывает обновления с бэкенда самостоятельно.
// Карточка монеты кликабельна — ведёт на детальную страницу с индикаторами.

import Link from "next/link";
import { useCallback, useEffect, useState } from "react";

import { API_URL } from "@/lib/api";
import { formatChange, formatCompact, formatPrice, formatTime } from "@/lib/format";
import type { AssetPrice } from "@/lib/types";

interface Props {
  initial: AssetPrice[];
}

const REFRESH_MS = 30_000;

export default function AssetList({ initial }: Props) {
  const [items, setItems] = useState<AssetPrice[]>(initial);
  const [error, setError] = useState<string | null>(null);
  const [updatedAt, setUpdatedAt] = useState<Date>(new Date());

  const refresh = useCallback(async () => {
    try {
      const res = await fetch(`${API_URL}/api/assets`, { cache: "no-store" });
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      const data: AssetPrice[] = await res.json();
      setItems(data);
      setError(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setUpdatedAt(new Date());
    }
  }, []);

  useEffect(() => {
    const id = setInterval(refresh, REFRESH_MS);
    return () => clearInterval(id);
  }, [refresh]);

  return (
    <section>
      <header className="mb-6 flex items-center justify-between">
        <div>
          <h2 className="text-xl font-semibold">Отслеживаемые монеты</h2>
          <p className="text-sm text-gray-400">
            Обновлено: {updatedAt.toLocaleTimeString("ru-RU")}
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
          <AssetCard key={a.coin_id} asset={a} />
        ))}
      </ul>
    </section>
  );
}

function AssetCard({ asset }: { asset: AssetPrice }) {
  const change = asset.change_24h;
  const up = change >= 0;
  const hasPrice = asset.price_usd > 0;

  return (
    <li>
      <Link
        href={`/assets/${asset.asset_id}`}
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
        <span
          className={`rounded-md px-2 py-1 text-sm font-medium ${
            up ? "bg-green-900/40 text-green-400" : "bg-red-900/40 text-red-400"
          }`}
        >
          {formatChange(change)}
        </span>
      </div>

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
      <div className="mt-3 text-xs text-gray-500">индикаторы и детали →</div>
      </Link>
    </li>
  );
}
