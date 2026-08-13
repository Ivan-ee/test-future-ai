package sentiment

import (
	"context"
	"errors"
	"testing"

	"test-future/internal/db"
	"test-future/internal/model"
	"test-future/internal/storage"
)

// fakeAIClient — тестовый AIClient: отдаёт заранее заданные результаты или ошибку.
type fakeAIClient struct {
	results []ScoreResult
	err     error
	calls   int
}

func (f *fakeAIClient) Score(_ context.Context, _ []string) ([]ScoreResult, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.results, nil
}

// newTestService — открывает in-memory БД и создаёт сервис с фейковым клиентом.
func newTestService(t *testing.T, client AIClient) (*Service, *storage.NewsItems) {
	t.Helper()
	ctx := context.Background()
	d, err := db.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("открытие тестовой БД: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	store := storage.NewNewsItems(d)
	return NewWithClient(client, store), store
}

// insertNews — помощник: вставляет новость и возвращает её с проставленным ID.
func insertNews(t *testing.T, ctx context.Context, store *storage.NewsItems, title, body string) model.NewsItem {
	t.Helper()
	assetID := int64(1)
	externalID := title
	n := model.NewsItem{
		AssetID: &assetID, SourceID: 2, ExternalID: externalID,
		Title: title, Body: body, Link: "https://example.com/" + externalID,
	}
	added, err := store.InsertMany(ctx, []model.NewsItem{n})
	if err != nil || added != 1 {
		t.Fatalf("вставка новости: added=%d err=%v", added, err)
	}
	// Находим именно эту новость по external_id, чтобы получить корректный ID.
	items, err := store.Unscored(ctx, 100)
	if err != nil {
		t.Fatalf("Unscored после вставки: %v", err)
	}
	for _, it := range items {
		if it.ExternalID == externalID {
			return it
		}
	}
	t.Fatalf("вставленная новость %s не найдена", externalID)
	return model.NewsItem{}
}

// TestScoreBatch_WritesResults — батч новостей оценивается, результаты пишутся в БД.
func TestScoreBatch_WritesResults(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	client := &fakeAIClient{results: []ScoreResult{
		{Score: 0.6, Summary: "позитивно"},
		{Score: -0.3, Summary: "умеренно негативно"},
	}}
	svc, store := newTestService(t, client)

	n1 := insertNews(t, ctx, store, "BTC soars", "Bitcoin hits 60k")
	n2 := insertNews(t, ctx, store, "Hack drains", "DeFi protocol exploited")

	scored, err := svc.ScoreBatch(ctx, []model.NewsItem{n1, n2})
	if err != nil {
		t.Fatalf("ScoreBatch: %v", err)
	}
	if scored != 2 {
		t.Errorf("хотели 2 оценённых, получили %d", scored)
	}

	// Проверяем, что сентимент записан.
	got, err := store.Unscored(ctx, 10)
	if err != nil {
		t.Fatalf("Unscored: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("хотели 0 неотсентиченных после оценки, получили %d", len(got))
	}
}

// TestScoreBatch_APIErrorDoesNotPanic — при ошибке API функция не падает,
// сентимент остаётся null.
func TestScoreBatch_APIErrorDoesNotPanic(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	client := &fakeAIClient{err: errors.New("OpenAI unavailable")}
	svc, store := newTestService(t, client)

	n1 := insertNews(t, ctx, store, "BTC soars", "Bitcoin up")

	scored, err := svc.ScoreBatch(ctx, []model.NewsItem{n1})
	if err != nil {
		t.Fatalf("ScoreBatch не должен возвращать ошибку при сбое API: %v", err)
	}
	if scored != 0 {
		t.Errorf("при ошибке API не должно быть оценённых, получили %d", scored)
	}

	// Новость осталась неотсентиченной.
	got, _ := store.Unscored(ctx, 10)
	if len(got) != 1 {
		t.Errorf("хотели 1 неотсентиченную, получили %d", len(got))
	}
}

// TestScoreBatch_CacheReusesByHash — две новости с одинаковым текстом оцениваются
// один раз (кэш), второй раз AIClient не вызывается.
func TestScoreBatch_CacheReusesByHash(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	client := &fakeAIClient{results: []ScoreResult{{Score: 0.5, Summary: "позитив"}}}
	svc, store := newTestService(t, client)

	// Первый батч: 1 новость → 1 вызов API.
	n1 := insertNews(t, ctx, store, "Same title", "Same body")
	svc.ScoreBatch(ctx, []model.NewsItem{n1})
	if client.calls != 1 {
		t.Fatalf("первый батч: хотели 1 вызов API, получили %d", client.calls)
	}

	// Вторая новость с тем же текстом (другой external_id).
	n2 := insertNews(t, ctx, store, "Same title copy", "Same body")
	// Подменим title на такой же, чтобы хэш совпал.
	n2.Title = "Same title"
	n2.Body = "Same body"
	svc.ScoreBatch(ctx, []model.NewsItem{n2})

	// Кэш должен был сработать — второй вызов API не нужен.
	if client.calls != 1 {
		t.Errorf("кэш: хотели 1 вызов API (кэш сработал), получили %d", client.calls)
	}
}

// TestScoreBatch_EmptyBatch — пустой батч → no-op, 0 вызовов API.
func TestScoreBatch_EmptyBatch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	client := &fakeAIClient{}
	svc, _ := newTestService(t, client)

	scored, err := svc.ScoreBatch(ctx, nil)
	if err != nil {
		t.Fatalf("ScoreBatch(nil): %v", err)
	}
	if scored != 0 {
		t.Errorf("хотели 0, получили %d", scored)
	}
	if client.calls != 0 {
		t.Errorf("хотели 0 вызовов API, получили %d", client.calls)
	}
}

// TestNew_NoApiKeyDisabled — без API-ключа сервис не активен.
func TestNew_NoApiKeyDisabled(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	d, _ := db.Open(ctx, ":memory:")
	defer func() { _ = d.Close() }()
	store := storage.NewNewsItems(d)

	svc := New("", "", "", store)
	if svc.Enabled() {
		t.Error("без API-ключа сервис должен быть выключен")
	}

	// ScoreBatch — no-op.
	scored, err := svc.ScoreBatch(ctx, []model.NewsItem{{ID: 1, Title: "x"}})
	if err != nil {
		t.Fatalf("ScoreBatch noop: %v", err)
	}
	if scored != 0 {
		t.Errorf("выключенный сервис не должен оценивать, получили %d", scored)
	}
}

// TestScoreBatch_ModelReturnsFewerResults — если модель вернула N-1 результатов,
// только N-1 новостей получают сентимент; последняя остаётся неотсентиченной.
func TestScoreBatch_ModelReturnsFewerResults(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	// Модель вернёт 1 результат вместо 2 — эмулирует рассинхрон.
	client := &fakeAIClient{results: []ScoreResult{{Score: 0.5, Summary: "ok"}}}
	svc, store := newTestService(t, client)

	n1 := insertNews(t, ctx, store, "title1", "body1")
	n2 := insertNews(t, ctx, store, "title2", "body2")

	scored, err := svc.ScoreBatch(ctx, []model.NewsItem{n1, n2})
	if err != nil {
		t.Fatalf("ScoreBatch: %v", err)
	}
	// Только первая оценена (1 результат из 2).
	if scored != 1 {
		t.Errorf("хотели 1 оценённую, получили %d", scored)
	}

	// Вторая осталась неотсентиченной (не зависла с zero-value score=0).
	got, _ := store.Unscored(ctx, 10)
	if len(got) != 1 {
		t.Fatalf("хотели 1 неотсентиченную, получили %d", len(got))
	}
}

// TestScoreBatch_NeutralScoreWithSummary — новость с score=0 и непустым summary
// корректно записывается (не выпадает как «не оценённая»).
func TestScoreBatch_NeutralScoreWithSummary(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	client := &fakeAIClient{results: []ScoreResult{{Score: 0, Summary: "нейтрально"}}}
	svc, store := newTestService(t, client)

	n1 := insertNews(t, ctx, store, "neutral", "nothing happened")

	scored, err := svc.ScoreBatch(ctx, []model.NewsItem{n1})
	if err != nil {
		t.Fatalf("ScoreBatch: %v", err)
	}
	if scored != 1 {
		t.Errorf("нейтральная новость должна быть оценена, получили %d", scored)
	}

	// Новость больше не в Unscored.
	got, _ := store.Unscored(ctx, 10)
	if len(got) != 0 {
		t.Errorf("хотели 0 неотсентиченных, получили %d", len(got))
	}
}
