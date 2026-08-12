# test-future

Прототип «предсказателя будущего» — системы краткосрочных криптопрогнозов с
прозрачной логикой: какие данные взяли, как проанализировали, какие цифры
сошлись и почему система пришла к выводу.

> Это прототип. Приоритет — прозрачность логики и демонстрируемость
> («цифры сходятся»), а не предсказательная сила. Прогноз может быть часто
> неверным — это нормально; главное, что система честно показывает точность
> и объясняет ошибки.

## Текущий слайс (T1): live-цены с CoinGecko

Минимальный вертикальный скелет, в котором live-цены криптовалют доходят от
реального источника (CoinGecko) до экрана пользователя. Запускаешь `make dev` —
открывается главная страница со списком 5 монет (BTC, ETH, SOL, BNB, XRP) и их
текущими ценами, обновляющимися автоматически. Прогнозов ещё нет — это просто
«живые цены из реального API». Ради этого слайса появляется вся инфраструктура:
Go-модуль, конфиг через env, SQLite с базовой схемой, адаптер источника,
HTTP API, Next.js-фронтенд, Makefile запуска двух процессов.

Данные сохраняются в БД (а не проксируются на лету) и виден `update_log` —
фундамент аудируемости.

## Стек

- **Бэкенд**: Go 1.25, chi (роутер), modernc.org/sqlite (БД без CGO),
  joho/godotenv (env).
- **Фронтенд**: Next.js 16 (App Router, TypeScript, Tailwind v4).
- **БД**: SQLite (WAL, foreign_keys).
- **Источник данных (T1)**: CoinGecko (free public tier, без ключа).

## Быстрый старт

```bash
cp .env.example .env       # при необходимости отредактируйте порты/URL
make dev                   # поднимает бэкенд (:8081) и фронтенд (:3001)
```

Главная страница: http://localhost:3001
API: http://localhost:8081/api/assets

## Команды

| Команда               | Действие                                              |
| --------------------- | ----------------------------------------------------- |
| `make dev`            | Поднять бэкенд и фронтенд параллельно                 |
| `make build`          | Собрать Go-бинарник в `bin/api`                       |
| `make db-init`        | Применить схему БД (миграция выполняется при старте)  |
| `make test`           | Запустить Go-тесты                                    |
| `make fmt`            | Отформатировать Go-код                                |
| `make clean`          | Удалить собранные артефакты и БД                      |

## Переменные окружения

См. `.env.example`. Значения по умолчанию подобраны так, чтобы `make dev`
работал из коробки.

| Переменная            | По умолчанию                          | Описание                          |
| --------------------- | ------------------------------------- | --------------------------------- |
| `OPENAI_API_KEY`      | _(пусто)_                             | Ключ OpenAI (нужен с T4)          |
| `COINGECKO_BASE_URL`  | `https://api.coingecko.com/api/v3`    | Базовый URL CoinGecko API         |
| `BACKEND_PORT`        | `8081`                                | Порт Go-бэкенда                   |
| `FRONTEND_PORT`       | `3001`                                | Порт Next.js фронтенда            |
| `DB_PATH`             | `data/testfuture.db`                  | Путь к файлу SQLite               |
| `FETCH_INTERVAL_MIN`  | `10`                                  | Период опроса источников, минут   |

## Архитектура

```
cmd/api/main.go              точка входа: конфиг → db → worker + http
internal/
  config/                    конфигурация из env (godotenv)
  db/                        открытие SQLite, embed-схема, сидинг
  model/                     доменные типы (Asset, PricePoint, UpdateLog…)
  source/coingecko/          адаптер CoinGecko: клиент + чистый маппинг
  storage/                   репозитории поверх sqlx (assets, price_points, …)
  server/                    HTTP API на chi + CORS
  worker/                    фоновый опрос источников по крону
frontend/                    Next.js 16 (App Router, TS, Tailwind v4)
  src/app/                   страницы (server components)
  src/components/            клиентские компоненты (AssetList с автообновлением)
  src/lib/                   клиент бэкенда, типы, форматирование
```

### Поток данных (T1)

```
CoinGecko API ──(worker, раз в 10 мин)──▶ price_points (с дедупом)
                                      └──▶ update_log (статус/ошибка)

GET /api/assets ──▶ assets + последняя price_point ──▶ Next.js (SSR + polling)
```

## Схема БД (T1)

- **assets** — отслеживаемые монеты (5: bitcoin, ethereum, solana, binancecoin, ripple).
- **sources** — реестр источников (на старте `coingecko`).
- **price_points** — точки цен: `(asset_id, ts, price_usd, market_cap, volume, source_id, change_24h)`.
  Дедуп по уникальному индексу `(asset_id, ts, source_id)`.
- **update_log** — история циклов обновления: `(source_slug, status, items_added, error, …)`.

Схема встроена в бинарник через `go:embed` (`internal/db/schema.sql`) и
применяется автоматически при старте.

## Тестирование

Тесты сосредоточены на чистой логике (без моков сети/БД/LLM), согласно
тестовой стратегии проекта:

- `internal/source/coingecko` — маппинг ответа API в точки цен
  (нормализация процентов, пропуск неизвестных монет).
- `internal/storage` — дедуп `price_points` по ключу (через in-memory SQLite).
- `internal/worker` — сквозной путь источник→БД→лог с фейковым источником.

```bash
make test              # все тесты
go test -v ./...       # подробный вывод
```

## Источники данных

- **CoinGecko** (T1): `/coins/markets` — текущая цена, market_cap, volume, change_24h.
- CoinPaprika + RSS + OpenAI (сентимент) — придут в T4.

## Roadmap

Линейная цепочка из 6 tracer-bullet слайсов (см. issue #1):

- [x] **T1** — Скелет: live-цены с CoinGecko доходят до UI _(этот слайс)_
- [ ] **T2** — Технические индикаторы (RSI/ROC/SMA/Volume) + тесты
- [ ] **T3** — Scoring-ядро — первый прогноз по 3 факторам (детерминированный)
- [ ] **T4** — Сентимент новостей (CoinPaprika + RSS + OpenAI) — 4-й фактор
- [ ] **T5** — Accuracy + атрибуция ошибок + адаптация весов
- [ ] **T6** — Worker-цикл + README с математикой + архитектура + smoke-тест
