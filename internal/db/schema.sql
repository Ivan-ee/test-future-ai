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
