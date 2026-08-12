package rss

import (
	"testing"
	"time"
)

// TestNewsMap_BasicFields — маппинг переносит все поля, asset_id=nil.
func TestNewsMap_BasicFields(t *testing.T) {
	t.Parallel()
	pub := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	raw := []RawNews{
		{
			ExternalID:  "https://example.com/n1",
			Title:       "Bitcoin soars",
			Summary:     "BTC hits new high",
			Link:        "https://example.com/n1",
			PublishedAt: pub,
		},
	}

	items := NewsMap(raw, 5)
	if len(items) != 1 {
		t.Fatalf("хотели 1 новость, получили %d", len(items))
	}
	if items[0].ExternalID != "https://example.com/n1" {
		t.Errorf("external_id: %q", items[0].ExternalID)
	}
	if items[0].Title != "Bitcoin soars" {
		t.Errorf("title: %q", items[0].Title)
	}
	if items[0].Body != "BTC hits new high" {
		t.Errorf("body: %q", items[0].Body)
	}
	if items[0].SourceID != 5 {
		t.Errorf("source_id: %d", items[0].SourceID)
	}
	if items[0].AssetID != nil {
		t.Errorf("хотели nil asset_id, получили %v", *items[0].AssetID)
	}
	if !items[0].PublishedAt.Equal(pub) {
		t.Errorf("published_at: хотели %v, получили %v", pub, items[0].PublishedAt)
	}
}

// TestNewsMap_EmptyInput — пустой вход → пустой выход.
func TestNewsMap_EmptyInput(t *testing.T) {
	t.Parallel()
	items := NewsMap(nil, 1)
	if len(items) != 0 {
		t.Errorf("хотели 0, получили %d", len(items))
	}
}

// TestNewsMap_MultipleItems — несколько элементов маппятся последовательно.
func TestNewsMap_MultipleItems(t *testing.T) {
	t.Parallel()
	raw := []RawNews{
		{ExternalID: "url1", Title: "t1", PublishedAt: time.Now()},
		{ExternalID: "url2", Title: "t2", PublishedAt: time.Now()},
		{ExternalID: "url3", Title: "t3", PublishedAt: time.Now()},
	}
	items := NewsMap(raw, 2)
	if len(items) != 3 {
		t.Fatalf("хотели 3, получили %d", len(items))
	}
	// Проверяем что все — с source_id=2.
	for _, n := range items {
		if n.SourceID != 2 {
			t.Errorf("source_id: хотели 2, получили %d", n.SourceID)
		}
	}
}
