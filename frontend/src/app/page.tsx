// Главная страница — серверный компонент (App Router).
// Начальные данные тянет с бэкенда через server-side fetch; автообновление
// раз в 30 секунд делает клиентская компонента AssetList.

import { fetchAssets } from "@/lib/api";
import AssetList from "@/components/AssetList";

export const dynamic = "force-dynamic"; // всегда свежие цены при загрузке

export default async function HomePage() {
  let initial: Awaited<ReturnType<typeof fetchAssets>> = [];
  let loadError: string | null = null;

  try {
    initial = await fetchAssets();
  } catch (e) {
    loadError = e instanceof Error ? e.message : String(e);
  }

  return (
    <main className="mx-auto max-w-5xl px-4 py-10">
      <header className="mb-10">
        <h1 className="text-3xl font-bold text-white">test-future</h1>
        <p className="mt-2 text-gray-400">
          Live-цены криптовалют из CoinGecko. Прогнозы появятся позже — пока
          фундамент: реальные цены доходят до экрана.
        </p>
      </header>

      {loadError ? (
        <div className="rounded-lg border border-red-900 bg-red-950/40 p-4 text-red-300">
          Не удалось загрузить данные с бэкенда ({loadError}). Убедитесь, что
          бэкенд запущен (make dev).
        </div>
      ) : (
        <AssetList initial={initial} />
      )}
    </main>
  );
}
