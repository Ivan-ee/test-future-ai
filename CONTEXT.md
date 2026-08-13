# test-future

Прототип системы краткосрочных криптопрогнозов (24ч, вверх/вниз) с прозрачной
логикой: прогноз — детерминированная функция от сохранённых данных, а не
случайность или ML. Приоритет — чтобы «цифры сходились» и систему можно было
аудировать, а не предсказательная сила.

## Language

### Монеты и рыночные данные

**Asset**:
Отслеживаемая криптовалюта (BTC, ETH, SOL, BNB, XRP). Идентифицируется внешним `coin_id` источника (например «bitcoin») и тикером `symbol` (например «BTC»).
_Avoid_: coin, token, currency

**PricePoint**:
Наблюдение цены актива в момент времени от конкретного источника: price, market_cap, volume, change_24h. Дедуп по (asset, момент, источник).
_Avoid_: quote, tick, candle

**IndicatorSnapshot**:
Последние посчитанные технические индикаторы по монете — одна строка на актив (UPSERT). Хранит RSI(14), ROC(10), SMA(7), SMA(20), VolumeSignal(14).
_Avoid_: metric, stat

**Source**:
Реестр внешних поставщиков данных (coingecko, coinpaprika, rss-coindesk, rss-cointelegraph). Сырые записи сохраняются с привязкой к источнику.
_Avoid_: provider, feed, connector

### Прогноз

**Forecast**:
Прогноз «вырастет или упадёт за 24 часа» по активу: direction (up/down), confidence [0.5..1.0], risk_note, argument_text, raw_score. Результат детерминированной функции от факторов.
_Avoid_: prediction, signal, bet

**Direction**:
Направление прогноза — «up» (вверх) или «down» (вниз). `direction = raw_score ≥ 0 ? up : down`.
_Avoid_: trend, side

**Confidence**:
Заявленная уверенность прогноза в диапазоне [0.5, 1.0]. Считается из |raw_score| со штрафом за противоречие факторов. Сравнивается с фактической точностью — так видно, переоценивает ли система себя.
_Avoid_: probability, certainty, score

**RawScore**:
Сумма взвешенных сигналов факторов: `Σ(signal_i × adjusted_weight_i)`. Знак определяет direction, модуль — confidence.
_Avoid_: result, total, sum

**Horizon**:
Горизонт прогноза — 24 часа. Через столько прогноз сверяется с фактом в resolve-цикле. Пока фиксирован.
_Avoid_: window, period, term

**ForecastStatus**:
Жизненный цикл прогноза: `active` (актуальный) → `superseded` (заменён более свежим) или `resolved` (сверен с фактом, есть Outcome). История сохраняется для аудита формулы.
_Avoid_: state, phase

### Факторы

**Factor**:
Один из четырёх входов прогноза: rsi, momentum, volume, sentiment. Каждый даёт сигнал [-1..+1] и вес; их комбинация = RawScore.
_Avoid_: input, variable, feature

**Signal**:
Числовое значение фактора в [-1..+1]: отрицательное → вниз, положительное → вверх, 0 → нейтрально. Преобразование индикатора (или сентимента) в нормированное число.
_Avoid_: reading, value, indicator value

**Contribution**:
Вклад фактора в RawScore: `signal × adjusted_weight`. Показывает, насколько каждый фактор сдвинул прогноз. Декомпозиция хранится в forecast_factors.
_Avoid_: impact, weight, effect

**BaseWeight**:
Исходный вес фактора из спеки (rsi 0.25, momentum 0.35, volume 0.15, sentiment 0.25; сумма 1.0). До нормировки и адаптации.
_Avoid_: default weight, initial weight

**AdjustedWeight**:
Вес фактора после нормировки под сумму присутствующих факторов и адаптации по hit-rate: `base_weight × clamp(EMA/0.5, 0.5, 1.5)`, с перенормировкой в 1.0. Именно он идёт в RawScore.
_Avoid_: final weight, normalized weight

**Contradiction**:
Ситуация, когда 2+ присутствующих факторов смотрят в сторону, противоположную direction. Каждый противоречащий фактор снижает confidence на 0.05.
_Avoid_: conflict, disagreement

### Сверка и обучение

**Outcome**:
Результат сверки прогноза с фактом через 24ч: result (hit/miss/neutral) + цены + culprit. Один к одному с прогнозом.
_Avoid_: resolution, verdict, result record

**Result**:
Итог сверки направления прогноза с фактическим движением цены. **hit** — совпало, |change| ≥ 0.5%; **miss** — не совпало, |change| ≥ 0.5%; **neutral** — |change| < 0.5% (слишком маленькое движение, не учитывается в обучении).
_Avoid_: outcome value, status, grade

**Resolve**:
Акт сверки прогноза старше горизонта (24ч) с текущей ценой: `change% = (цена_сейчас / цена_на_момент_прогноза − 1) × 100` → Result. Делается resolve-циклом раз в час.
_Avoid_: check, verify, evaluate

**Culprit**:
Фактор, сильнее всего «виновный» в промахе (при miss): максимальный по модулю вклад, чей знак противоречит факту. При hit фиксируется ведущий фактор. Человекочитаемое объяснение — culprit_explanation.
_Avoid_: blamed factor, wrong factor, driver

**HitRateEMA**:
Экспоненциальное среднее (α=0.2) доли совпадений знака сигнала фактора с фактом. Старт 0.5 (нейтрально). Хранится в factor_stats по (asset, factor) и двигает AdjustedWeight — система самообучается без ML.
_Avoid_: accuracy rate, success rate, hit rate

**Adaptation**:
Поправка весов факторов на основе HitRateEMA перед каждым новым прогнозом: фактор, что часто ошибается, получает понижение. Лёгкая альтернатива ML-оптимизации.
_Avoid_: learning, tuning, optimization

### Данные и новости

**NewsItem**:
Новость из внешнего источника (CoinPaprika, RSS). Дедуп по (source, external_id). Сентимент проставляется позже через OpenAI (nullable до оценки).
_Avoid_: article, post, headline

**Sentiment**:
Оценка тональности батча новостей через OpenAI: score [-1..+1] + summary. Единственное применение ИИ в системе. Без OPENAI_API_KEY выключен — прогноз на 3 факторах.
_Avoid_: mood, tone, opinion

**UpdateLog**:
Запись одного цикла обновления источника: статус (ok/error), items_added, ошибка. Журнал для диагностики проблем со сбором данных.
_Avoid_: log entry, audit record, run record
