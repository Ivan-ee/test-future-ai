-- Схема БД test-future (T1: скелет с live-ценами).
--
-- Слоистая модель: активы, источники, точки цен, журнал обновлений.
-- Дедуп точек цен — по уникальному индексу (asset_id, ts, source_id).

-- Отслеживаемые криптовалюты.
CREATE TABLE IF NOT EXISTS assets (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    coin_id    TEXT    NOT NULL UNIQUE,           -- внешний идентификатор, напр. "bitcoin"
    symbol     TEXT    NOT NULL,                  -- тикер, напр. "BTC"
    name       TEXT    NOT NULL,                  -- название, напр. "Bitcoin"
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Реестр источников данных.
CREATE TABLE IF NOT EXISTS sources (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    slug       TEXT    NOT NULL UNIQUE,           -- стабильный слаг, напр. "coingecko"
    name       TEXT    NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Точки цен: одна запись на (актив, момент, источник).
CREATE TABLE IF NOT EXISTS price_points (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    asset_id    INTEGER NOT NULL REFERENCES assets(id),
    ts          DATETIME NOT NULL,                -- момент наблюдения цены
    price_usd   REAL     NOT NULL,
    market_cap  REAL     NOT NULL DEFAULT 0,
    volume      REAL     NOT NULL DEFAULT 0,
    source_id   INTEGER NOT NULL REFERENCES sources(id),
    change_24h  REAL     NOT NULL DEFAULT 0,
    inserted_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (asset_id, ts, source_id)              -- дедуп по ключу наблюдения
);

CREATE INDEX IF NOT EXISTS idx_price_points_asset_ts
    ON price_points (asset_id, ts DESC);

-- Журнал обновлений источников: когда/сколько/успешно ли.
CREATE TABLE IF NOT EXISTS update_log (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    source_slug  TEXT    NOT NULL,
    status       TEXT    NOT NULL,                -- "ok" | "error"
    items_added  INTEGER NOT NULL DEFAULT 0,
    error        TEXT    NOT NULL DEFAULT '',
    started_at   DATETIME NOT NULL,
    finished_at  DATETIME NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_update_log_source_started
    ON update_log (source_slug, started_at DESC);

-- Снапшоты посчитанных индикаторов: одна строка на актив (последние значения).
-- T2: RSI(14), ROC(10), SMA(7), SMA(20), VolumeSignal(14д).
-- UPSERT по asset_id при каждом цикле worker — храним только актуальный срез.
CREATE TABLE IF NOT EXISTS indicator_snapshots (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    asset_id       INTEGER NOT NULL UNIQUE REFERENCES assets(id),  -- одна строка на монету
    source_id      INTEGER NOT NULL REFERENCES sources(id),
    rsi            REAL     NOT NULL DEFAULT 0,
    roc            REAL     NOT NULL DEFAULT 0,
    sma_7          REAL     NOT NULL DEFAULT 0,
    sma_20         REAL     NOT NULL DEFAULT 0,
    volume_signal  REAL     NOT NULL DEFAULT 0,
    calculated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Прогнозы «вверх/вниз за 24ч» по активу (T3).
-- Каждый цикл пересчёта добавляет новую строку; предыдущий активный прогноз
-- переводится в статус superseded (история для аудита формулы).
CREATE TABLE IF NOT EXISTS forecasts (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    asset_id       INTEGER NOT NULL REFERENCES assets(id),
    created_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    horizon_hours  INTEGER NOT NULL DEFAULT 24,
    direction      TEXT    NOT NULL,                -- "up" | "down"
    confidence     REAL    NOT NULL,                -- [0.5, 1.0]
    risk_note      TEXT    NOT NULL DEFAULT '',
    argument_text  TEXT    NOT NULL DEFAULT '',
    raw_score      REAL    NOT NULL,
    status         TEXT    NOT NULL DEFAULT 'active' -- "active" | "superseded"
);

CREATE INDEX IF NOT EXISTS idx_forecasts_asset_created
    ON forecasts (asset_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_forecasts_status
    ON forecasts (status);

-- Декомпозиция вклада факторов в прогноз (T3). Прозрачность формулы: какой
-- сигнал, какой вес (базовый и нормированный), какой вклад и описание.
CREATE TABLE IF NOT EXISTS forecast_factors (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    forecast_id      INTEGER NOT NULL REFERENCES forecasts(id) ON DELETE CASCADE,
    name             TEXT    NOT NULL,               -- "rsi" | "momentum" | "volume"
    signal           REAL    NOT NULL,               -- [-1..1]
    base_weight      REAL    NOT NULL,
    adjusted_weight  REAL    NOT NULL,
    contribution     REAL    NOT NULL,               -- signal × adjusted_weight
    detail           TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_forecast_factors_forecast
    ON forecast_factors (forecast_id);

-- Новости из внешних источников (T4): CoinPaprika и RSS (CoinDesk/Cointelegraph).
-- Дедуп — по уникальному ключу (source_id, external_id). Сентимент проставляется
-- позже через OpenAI (nullable до оценки).
CREATE TABLE IF NOT EXISTS news_items (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    asset_id          INTEGER REFERENCES assets(id),     -- nullable: связь с монетой
    source_id         INTEGER NOT NULL REFERENCES sources(id),
    external_id       TEXT    NOT NULL,                  -- идентификатор у источника
    title             TEXT    NOT NULL DEFAULT '',
    body              TEXT    NOT NULL DEFAULT '',
    link              TEXT    NOT NULL DEFAULT '',
    published_at      DATETIME NOT NULL,
    sentiment_score   REAL,                              -- nullable: -1..1
    sentiment_summary TEXT,                              -- nullable: короткое резюме
    inserted_at       DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (source_id, external_id)
);

CREATE INDEX IF NOT EXISTS idx_news_items_asset_published
    ON news_items (asset_id, published_at DESC);
CREATE INDEX IF NOT EXISTS idx_news_items_unscored
    ON news_items (source_id) WHERE sentiment_score IS NULL;

-- Результаты сверки прогнозов с фактом (T5). Одна строка на прогноз, который
-- достиг горизонта (24ч) и был сверен с фактическим изменением цены.
-- result: hit (направление совпало) | miss (не совпало, |change| ≥ 0.5%)
--       | neutral (|change| < 0.5% — слишком маленькое движение).
-- culprit_factor — фактор, «виновный» в промахе (при miss) или ведущий (при hit).
CREATE TABLE IF NOT EXISTS outcomes (
    forecast_id         INTEGER PRIMARY KEY REFERENCES forecasts(id) ON DELETE CASCADE,
    resolved_at         DATETIME NOT NULL,
    actual_direction    TEXT    NOT NULL,                  -- "up" | "down"
    result              TEXT    NOT NULL,                  -- "hit" | "miss" | "neutral"
    price_at_forecast   REAL    NOT NULL,
    price_at_resolution REAL    NOT NULL,
    price_change_pct    REAL    NOT NULL,                  -- (resolution/forecast − 1) × 100
    culprit_factor      TEXT    NOT NULL DEFAULT '',
    culprit_explanation TEXT    NOT NULL DEFAULT ''
);

-- Скользящая статистика точности каждого фактора по монете (T5). hit_rate_ema —
-- экспоненциальное среднее доли совпадений знака сигнала с фактом (α=0.2).
-- Используется для адаптации весов: чем чаще фактор угадывает, тем больше его вес.
CREATE TABLE IF NOT EXISTS factor_stats (
    asset_id      INTEGER NOT NULL REFERENCES assets(id),
    factor        TEXT    NOT NULL,                        -- "rsi" | "momentum" | "volume" | "sentiment"
    hit_rate_ema  REAL    NOT NULL DEFAULT 0.5,            -- [0..1], старт с 0.5 (нейтрально)
    samples       INTEGER NOT NULL DEFAULT 0,
    updated_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (asset_id, factor)
);
