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
