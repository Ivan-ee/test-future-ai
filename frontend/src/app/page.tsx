// Главная страница — серверный компонент (App Router).
// Начальные данные (цены + прогнозы + сводка точности) тянет с бэкенда через
// server-side fetch; автообновление раз в 30 секунд делает клиентская компонента AssetList.

import { fetchAccuracy, fetchAssets, fetchForecasts } from "@/lib/api";
import AccuracyWidget from "@/components/AccuracyWidget";
import AssetList from "@/components/AssetList";

export const dynamic = "force-dynamic"; // всегда свежие цены при загрузке

export default async function HomePage() {
  let initialAssets: Awaited<ReturnType<typeof fetchAssets>> = [];
  let initialForecasts: Awaited<ReturnType<typeof fetchForecasts>> = [];
  let accuracy: Awaited<ReturnType<typeof fetchAccuracy>> | null = null;
  let loadError: string | null = null;

  try {
    [initialAssets, initialForecasts, accuracy] = await Promise.all([
      fetchAssets(),
      fetchForecasts().catch(() => [] as Awaited<ReturnType<typeof fetchForecasts>>),
      fetchAccuracy().catch(() => null),
    ]);
  } catch (e) {
    loadError = e instanceof Error ? e.message : String(e);
  }

  return (
    <main className="mx-auto max-w-5xl px-4 py-10">
      <header className="mb-10">
        <h1 className="text-3xl font-bold text-white">test-future</h1>
        <p className="mt-2 text-gray-400">
          Live-цены криптовалют из CoinGecko, технические индикаторы (RSI/ROC/
          SMA/объём) и прозрачные прогнозы «вверх/вниз за 24ч». Прогноз считается
          детерминированно из сохранённых индикаторов по понятной формуле.
        </p>
      </header>

      {loadError ? (
        <div className="rounded-lg border border-red-900 bg-red-950/40 p-4 text-red-300">
          Не удалось загрузить данные с бэкенда ({loadError}). Убедитесь, что
          бэкенд запущен (make dev).
        </div>
      ) : (
        <>
          <AccuracyWidget summary={accuracy} />
          <AssetList initialAssets={initialAssets} initialForecasts={initialForecasts} />
        </>
      )}
    </main>
  );
}
