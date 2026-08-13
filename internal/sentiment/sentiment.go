// Package sentiment — оценка сентимента новостей через OpenAI.
//
// Единственное применение ИИ в проекте: берёт батч заголовков+lead новостей,
// вызывает модель, на выходе для каждой новости — score [-1..1] и короткое
// summary на русском. Результат пишется в news_items.sentiment_score/summary.
//
// Устойчивость к ошибкам API: при сбое сентимент остаётся null, прогноз
// считается без этого фактора (graceful degradation). Кэш по sha256(title+body)
// — чтобы не платить дважды за одинаковый текст.
package sentiment

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"

	openai "github.com/sashabaranov/go-openai"

	"test-future/internal/model"
	"test-future/internal/storage"
)

// maxBodyLen — сколько символов body отправлять в промпт (чтобы не раздувать токены).
const maxBodyLen = 500

// batchLimit — максимум новостей в одном запросе к OpenAI.
const batchLimit = 20

// ScoreResult — результат оценки одной новости.
type ScoreResult struct {
	Score   float64 // [-1..1]
	Summary string  // короткое резюме сентимента
}

// AIClient абстрагирует вызов LLM для тестируемости. Реализация по умолчанию —
// openaiClient поверх sashabaranov/go-openai.
type AIClient interface {
	Score(ctx context.Context, headlines []string) ([]ScoreResult, error)
}

// Service координирует батчевую оценку сентимента: читает неотсентиченные
// новости, вызывает AIClient, пишет результат в БД с кэшем по хэшу текста.
type Service struct {
	client  AIClient
	store   *storage.NewsItems
	mu      sync.RWMutex
	cache   map[string]ScoreResult // hash(title+body) → результат
	enabled bool
}

// New создаёт сервис с реальным OpenAI-совместимым клиентом. apiKey пустой →
// noop-режим. baseURL задаёт эндпоинт провайдера (по умолчанию api.openai.com,
// но можно указать OpenAI-совместимый — Kimi/Moonshot, DeepSeek, OpenRouter,
// локальный Ollama). model — имя модели для оценки сентимента.
func New(apiKey, baseURL, model string, store *storage.NewsItems) *Service {
	if apiKey == "" {
		return &Service{store: store, cache: map[string]ScoreResult{}, enabled: false}
	}
	return &Service{
		client:  newOpenAIClient(apiKey, baseURL, model),
		store:   store,
		cache:   map[string]ScoreResult{},
		enabled: true,
	}
}

// NewWithClient позволяет внедрить тестовую/моковую реализацию AIClient.
func NewWithClient(client AIClient, store *storage.NewsItems) *Service {
	return &Service{client: client, store: store, cache: map[string]ScoreResult{}, enabled: true}
}

// Enabled — true, если сервис активен (есть API-ключ или клиент).
func (s *Service) Enabled() bool { return s.enabled }

// ScoreBatch берёт батч неотсентиченных новостей, оценивает их сентимент через
// AIClient и пишет результат в БД. При ошибке API — логирует warning и
// возвращается без паники (сентимент остаётся null).
func (s *Service) ScoreBatch(ctx context.Context, items []model.NewsItem) (scored int, err error) {
	if !s.enabled || len(items) == 0 {
		return 0, nil
	}

	// Сначала — кэш: если для текста уже есть результат, переиспользуем.
	type pending struct {
		idx      int
		hash     string
		headline string
	}
	var toScore []pending
	results := make([]ScoreResult, len(items))
	evaluated := make([]bool, len(items)) // реально оценена (кэш или API)

	for i, n := range items {
		h := textHash(n.Title, n.Body)
		s.mu.RLock()
		cached, ok := s.cache[h]
		s.mu.RUnlock()
		if ok {
			results[i] = cached
			evaluated[i] = true
			continue
		}
		toScore = append(toScore, pending{idx: i, hash: h, headline: headline(n)})
	}

	// Если есть, что отправить в LLM — батчим по batchLimit.
	for start := 0; start < len(toScore); start += batchLimit {
		end := start + batchLimit
		if end > len(toScore) {
			end = len(toScore)
		}
		batch := toScore[start:end]

		headlines := make([]string, len(batch))
		for j, p := range batch {
			headlines[j] = p.headline
		}

		scores, apiErr := s.client.Score(ctx, headlines)
		if apiErr != nil {
			// Не валим весь батч: логируем и прерываем (следующий цикл попробует снова).
			log.Printf("sentiment: ошибка OpenAI, сентимент не проставлен (%d новостей): %v", len(items)-scored, apiErr)
			break
		}

		// Модель должна вернуть по одному результату на каждую новость батча.
		// Если длина не совпала — логируем, но пишем сколько есть.
		if len(scores) != len(batch) {
			log.Printf("sentiment: модель вернула %d результатов на %d новостей — пишем по минимуму",
				len(scores), len(batch))
		}
		n := len(scores)
		if n > len(batch) {
			n = len(batch)
		}
		for j := range n {
			p := batch[j]
			results[p.idx] = scores[j]
			evaluated[p.idx] = true
			s.mu.Lock()
			s.cache[p.hash] = scores[j]
			s.mu.Unlock()
		}
	}

	// Пишем результаты в БД — только реально оцененные новости.
	for i, n := range items {
		if !evaluated[i] {
			// Не оценено (ошибка API или не попало в батч) — пропускаем, остаётся null.
			continue
		}
		if err := s.store.SetSentiment(ctx, n.ID, results[i].Score, results[i].Summary); err != nil {
			log.Printf("sentiment: запись сентимента для news %d: %v", n.ID, err)
			continue
		}
		scored++
	}
	return scored, nil
}

// textHash — sha256 от конкатенации title+body для кэширования.
func textHash(title, body string) string {
	h := sha256.Sum256([]byte(title + "\n" + body))
	return hex.EncodeToString(h[:])
}

// headline — строка для промпта: заголовок + первые maxBodyLen символов body.
func headline(n model.NewsItem) string {
	body := n.Body
	if len(body) > maxBodyLen {
		body = body[:maxBodyLen]
	}
	return fmt.Sprintf("%s. %s", n.Title, body)
}

// --- реализация AIClient поверх go-openai ---

type openaiClient struct {
	client *openai.Client
	model  string
}

// newOpenAIClient собирает клиент с кастомным BaseURL — это позволяет работать
// с любым OpenAI-совместимым провайдером (Kimi/Moonshot, DeepSeek, OpenRouter,
// локальный Ollama), а не только с api.openai.com.
func newOpenAIClient(apiKey, baseURL, model string) *openaiClient {
	cfg := openai.DefaultConfig(apiKey)
	if baseURL != "" {
		cfg.BaseURL = baseURL
	}
	return &openaiClient{client: openai.NewClientWithConfig(cfg), model: model}
}

// sentimentResponse — ожидаемый JSON из ответа модели.
type sentimentResponse struct {
	Results []ScoreResult `json:"results"`
}

// Score отправляет батч заголовков в модель с инструкцией вернуть JSON
// с массивом результатов (score + summary для каждого по индексу).
func (c *openaiClient) Score(ctx context.Context, headlines []string) ([]ScoreResult, error) {
	prompt := buildPrompt(headlines)
	model := c.model
	if model == "" {
		model = openai.GPT4oMini
	}
	resp, err := c.client.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
		Model: model,
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleSystem, Content: systemPrompt()},
			{Role: openai.ChatMessageRoleUser, Content: prompt},
		},
		Temperature: 0.0,
		ResponseFormat: &openai.ChatCompletionResponseFormat{
			Type: openai.ChatCompletionResponseFormatTypeJSONObject,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("запрос OpenAI: %w", err)
	}
	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("OpenAI вернул пустой ответ")
	}

	content := resp.Choices[0].Message.Content
	var sr sentimentResponse
	if err := json.Unmarshal([]byte(content), &sr); err != nil {
		return nil, fmt.Errorf("разбор ответа OpenAI: %w (content=%s)", err, content)
	}
	return sr.Results, nil
}

func systemPrompt() string {
	return strings.TrimSpace(`Ты — анализатор сентимента криптовалютных новостей.
Для каждой новости верни score от -1 (очень негативно) до +1 (очень позитивно),
где 0 — нейтрально, и короткое summary (одно предложение на русском).
Ответ — строго JSON вида: {"results":[{"score":0.5,"summary":"..."}]}
Количество элементов в results должно совпадать с количеством переданных новостей.`)
}

func buildPrompt(headlines []string) string {
	var b strings.Builder
	b.WriteString("Оцени сентимент каждой новости. Возвращай результаты по порядку.\n\n")
	for i, h := range headlines {
		fmt.Fprintf(&b, "%d. %s\n", i+1, h)
	}
	return b.String()
}
