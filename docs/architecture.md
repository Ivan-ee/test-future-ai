# Архитектура test-future

Прототип системы краткосрочных криптопрогнозов (24ч, вверх/вниз). Один
процесс — один бинарник: HTTP-сервер и worker-цикл в горутинах рядом, общая
SQLite-БД.

> Приоритет — прозрачность логики («цифры сходятся»), а не предсказательная
> сила. Прогноз может быть часто неверным; главное — система честно показывает
> точность и объясняет ошибки.

---

## Компоненты

```
cmd/api/main.go              точка входа: конфиг → db → worker + http → graceful shutdown
internal/
  config/                    конфигурация из env (godotenv)
  db/                        открытие SQLite, embed-схема, сидинг
  model/                     доменные типы (Asset, PricePoint, Forecast, Outcome…)
  source/
    coingecko/               адаптер CoinGecko: цены + рыночные графики
    coinpaprika/             адаптер CoinPaprika: новости
    rss/                     адаптер RSS (CoinDesk, Cointelegraph)
  indicator/                 технические индикаторы: RSI, ROC, SMA, VolumeSignal
  scoring/                   детерминированное ядро прогноза (4 фактора → вверх/вниз)
  sentiment/                 оценка сентимента новостей через OpenAI (единственный ИИ)
  accuracy/                  сверка прогнозов с фактом, атрибуция, адаптация весов
  storage/                   репозитории поверх SQLite
  server/                    HTTP API на chi + CORS
  worker/                    фоновый цикл: fetch → индикаторы → прогноз → resolve
frontend/                    Next.js 16 (App Router, TS, Tailwind v4)
  src/app/                   страницы: главная, детальная карточка, прогноз
  src/components/            клиентские виджеты (AssetList, AccuracyWidget)
  src/lib/                   клиент бэкенда, типы, форматирование
```

---

## Главный пайплайн: источники → прогноз → UI

Один процесс. Worker — это горутина с тремя тикерами; HTTP-сервер — вторая
горутина. Оба работают с одной БД.

```
                    ┌─────────────────────────────────────────────────┐
                    │                 ИСТОЧНИКИ                        │
                    ├─────────────────────────────────────────────────┤
                    │  CoinGecko   CoinPaprika   RSS (CoinDesk,       │
                    │  цены+график  новости       Cointelegraph)       │
                    └──────────────┬──────────────────────────────────┘
                                   │  fetch (раз в 10 мин)
                                   ▼
                    ┌──────────────────────────────────┐
                    │            SQLite (БД)            │
                    │  price_points · news_items        │
                    └──────────────┬───────────────────┘
                                   │  ряды цен/объёмов
                                   ▼
                    ┌──────────────────────────────────┐
                    │       indicator-слой (чистый Go)  │
                    │  RSI(14) · ROC(10) · SMA(7/20)    │
                    │  · VolumeSignal(14)               │
                    └──────────────┬───────────────────┘
                                   │  indicator_snapshots
                                   ▼
                    ┌──────────────────────────────────┐
                    │  sentiment (OpenAI, опционально)  │
                    │  средний сентимент новостей за 24ч│
                    └──────────────┬───────────────────┘
                                   │
                                   ▼
                    ┌──────────────────────────────────┐
                    │      scoring-ядро (4 фактора)     │
                    │  signal × weight → raw_score      │
                    │  → direction, confidence          │
                    └──────────────┬───────────────────┘
                                   │  forecasts + forecast_factors
                                   ▼
                    ┌──────────────────────────────────┐
                    │          HTTP API (chi)           │
                    │  /api/forecasts · /api/accuracy   │
                    └──────────────┬───────────────────┘
                                   │  JSON
                                   ▼
                    ┌──────────────────────────────────┐
                    │     Next.js UI (SSR + polling)    │
                    │  главная · карточка · прогноз     │
                    └──────────────────────────────────┘
```

### Расписание worker-цикла

Три независимых тикера в одной горутине (`internal/worker/worker.go`, `Run`):

| Тикер             | Период        | Что делает                                                    |
| ----------------- | ------------- | ------------------------------------------------------------- |
| `priceTicker`     | 10 мин\*      | CoinGecko markets + market\_chart; новости; сентимент          |
| `forecastTicker`  | 1 час         | Пересчёт прогноза по каждой монете                           |
| `resolveTicker`   | 1 час         | Сверка прогнозов старше 24ч с фактом + обновление factor\_stats |

\* Настраивается через `FETCH_INTERVAL_MIN`. Прогноз и resolve — фиксированные
1 час (по спеке). При старте первый цикл каждого вида выполняется сразу — UI
наполняется данными без ожидания.

### Холодный старт

При запуске worker сразу выполняет `fetchOnce` → `computeForecasts` →
`resolveOnce`. После первого цикла в БД есть цены по 5 монетам, индикаторы и
первые прогнозы — главная страница не пустая.

### Graceful shutdown

По `SIGINT`/`SIGTERM` (`cmd/api/main.go`): сначала отменяется root-context
(worker завершает текущий шаг), затем `http.Server.Shutdown` с таймаутом 10с.
БД закрывается через `defer`. Данные не теряются — все записи транзакционны.

---

## Петля обучения: resolve → атрибуция → адаптация → следующий прогноз

Это замкнутый цикл, благодаря которому система «учится на промахах». Петля
работает на уже существующих данных — никаких отдельных фаз.

```
   прогноз создан (forecasts.status = active)
              │
              │  ─── ждём 24ч (горизонт прогноза) ───►
              ▼
   ┌─────────────────────────────────────────────┐
   │  resolve-цикл (раз в час)                    │
   │  прогнозы старше 24ч без outcome             │
   └──────────────────────┬──────────────────────┘
                          │
          ┌───────────────┴───────────────┐
          ▼                               ▼
   цена на момент                  текущая цена
   прогноза                        (price_points)
          └───────────────┬───────────────┘
                          ▼
   ┌─────────────────────────────────────────────┐
   │  accuracy.Resolve                            │
   │  change% = (resolution/forecast − 1) × 100   │
   │  |change| < 0.5% → neutral (не учитывается)  │
   │  иначе → hit / miss по совпадению направления│
   └──────────────────────┬──────────────────────┘
                          │
        ┌─────────────────┴─────────────────┐
        ▼                                   ▼
   outcome записан                   атрибуция
   (outcomes)                        при miss → «виновный» фактор
        │                            при hit  → ведущий фактор
        │                                   │
        │                     ┌─────────────┘
        ▼                     ▼
   ┌─────────────────────────────────────────────┐
   │  accuracy.UpdateHitRateEMA (по каждому       │
   │  фактору прогноза)                           │
   │  EMA = 0.8×EMA_old + 0.2×(знак совпал? 1:0)  │
   │  → factor_stats.hit_rate_ema                 │
   └──────────────────────┬──────────────────────┘
                          │
                          ▼
   ┌─────────────────────────────────────────────┐
   │  ПРИ СЛЕДУЮЩЕМ ПРОГНОЗЕ (computeForecasts)   │
   │  accuracy.AdjustedWeight:                    │
   │  w_adj = base × clamp(EMA/0.5, 0.5, 1.5)     │
   │  фактор, что часто ошибается → меньше вес    │
   └──────────────────────┬──────────────────────┘
                          │
                          ▼
              новый прогноз с адаптированными весами
              (forecasts + forecast_factors)
                          │
                          └───► петля повторяется
```

### Что где живёт

| Этап петли        | Код                                     | Данные                          |
| ----------------- | --------------------------------------- | ------------------------------- |
| Создание прогноза | `worker.computeOneForecast`             | `forecasts`, `forecast_factors` |
| Resolve           | `worker.resolveOnce` → `accuracy.Resolve` | `outcomes`                      |
| Атрибуция         | `accuracy.AttributeMiss/AttributeHit`   | `outcomes.culprit_*`            |
| Обучение EMA      | `worker.updateFactorStats` → `accuracy.UpdateHitRateEMA` | `factor_stats`      |
| Адаптация весов   | `worker.adaptedWeights` → `accuracy.AdjustedWeight` | (передаётся в scoring) |

---

## Поток данных в деталях

### Fetch-цикл (каждые 10 минут)

1. **CoinGecko `/coins/markets`** — текущие цены 5 монет → `price_points`
   (дедуп по `asset_id, ts, source_id`).
2. **CoinGecko `/coins/{id}/market_chart`** — ряды цен/объёмов за 30 дней по
   каждой монете → `price_points`. Затем расчёт индикаторов из БД (единый
   источник правды) → `indicator_snapshots` (UPSERT, одна строка на монету).
3. **CoinPaprika + RSS** — новости → `news_items` (дедуп по `source_id,
   external_id`).
4. **Сентимент** — батч неотсентиченных новостей через OpenAI →
   `news_items.sentiment_score/summary`. Без `OPENAI_API_KEY` — пропускается.

### Прогноз-цикл (каждый час)

1. Читает `indicator_snapshots` + средний сентимент новостей за 24ч.
2. Преобразует индикаторы в сигналы `[-1..1]` (`scoring/signals.go`).
3. Адаптирует веса факторов по `factor_stats.hit_rate_ema` (`accuracy.AdjustedWeight`).
4. Считает `scoring.Forecast` → направление, уверенность, риск, аргументация.
5. Сохраняет в `forecasts` (предыдущий active → `superseded`) + `forecast_factors`.

### HTTP API

| Метод | Путь                   | Назначение                                    |
| ----- | ---------------------- | --------------------------------------------- |
| GET   | `/api/health`          | liveness                                      |
| GET   | `/api/assets`          | список монет с последней ценой                |
| GET   | `/api/assets/{id}`     | карточка монеты: цена + индикаторы            |
| GET   | `/api/forecasts`       | последние прогнозы по всем монетам            |
| GET   | `/api/forecasts/{asset}` | детальный прогноз: факторы, данные, история |
| GET   | `/api/accuracy`        | глобальная сводка точности + per-factor       |

---

## Схема БД

```
assets ──< price_points >── sources
  │           │
  │           └── дедуп: UNIQUE(asset_id, ts, source_id)
  │
  ├──< indicator_snapshots   (1 строка на монету, UPSERT)
  │
  ├──< forecasts ──< forecast_factors
  │       │
  │       └── status: active → superseded | resolved
  │
  ├──> outcomes (1:1 с resolved-прогнозом)
  │       └── result: hit | miss | neutral
  │       └── culprit_factor + culprit_explanation (атрибуция)
  │
  └──< factor_stats   PK(asset_id, factor)
          └── hit_rate_ema: [0..1], старт 0.5, α=0.2
          └── используется для адаптации весов

news_items >── sources
  └── asset_id nullable (связь с монетой)
  └── sentiment_score / sentiment_summary nullable (до оценки OpenAI)
  └── дедуп: UNIQUE(source_id, external_id)

update_log   ── журнал циклов fetch (статус, items_added, error)
```

Полная схема — в `internal/db/schema.sql` (встроена в бинарник через
`go:embed`, применяется при старте).

---

## Что использовано из ИИ

Единственное применение ИИ — **оценка сентимента новостей через OpenAI**
(`internal/sentiment/`). Всё остальное — детерминированная логика на чистом Go:

- индикаторы — формулы (Уайлдер для RSI, классические ROC/SMA);
- scoring — линейная комбинация сигналов;
- resolve/атрибуция/адаптация — арифметика.

Сентимент выключается без `OPENAI_API_KEY` — прогноз работает на 3 факторах
(graceful degradation). Кэш по `sha256(title+body)` исключит повторные запросы
за один и тот же текст.
