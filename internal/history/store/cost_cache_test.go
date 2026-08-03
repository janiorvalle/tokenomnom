package store

import (
	"bytes"
	"path/filepath"
	"testing"
)

func TestSessionCostCachePersistsAndPartitionsExactKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), DatabaseName)
	database, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	source := sourceRef(filepath.Join(t.TempDir(), "session.jsonl"), "provider_live")
	result, err := database.ApplySource(extraction("cache-session", "cache-session", source, prompt("native:p", "p", "cache", 1)), head(source, "digest-a", 10, 1), ApplyReplace)
	if err != nil {
		database.Close()
		t.Fatal(err)
	}
	candidates, err := database.RawCandidates(result.SessionID, "")
	if err != nil || len(candidates) != 1 || candidates[0].SourceHeadID == nil {
		database.Close()
		t.Fatalf("cache candidates = %+v, err=%v", candidates, err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	key := SessionCostCacheKey{
		SessionID: result.SessionID, LocationID: *candidates[0].SourceHeadID,
		ContentSHA256: candidates[0].ContentSHA256, ContentSize: candidates[0].Size,
		PricingFingerprint: "pricing-a", CandidateContext: "context-a",
		WindowSince: "2026-08-02T00:00:00Z", WindowUntil: "2026-08-02T23:59:59Z",
	}
	reader, err := OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"cost":42}`)
	if err := reader.PutSessionCostCache(key, payload); err != nil {
		reader.Close()
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, found, err := reopened.GetSessionCostCache(key)
	if err != nil || !found || !bytes.Equal(got, payload) {
		t.Fatalf("persisted cache = %q found=%v err=%v", got, found, err)
	}
	for name, changed := range map[string]SessionCostCacheKey{
		"digest":            func() SessionCostCacheKey { value := key; value.ContentSHA256 = "digest-b"; return value }(),
		"size":              func() SessionCostCacheKey { value := key; value.ContentSize++; return value }(),
		"pricing":           func() SessionCostCacheKey { value := key; value.PricingFingerprint = "pricing-b"; return value }(),
		"candidate-context": func() SessionCostCacheKey { value := key; value.CandidateContext = "context-b"; return value }(),
		"window":            func() SessionCostCacheKey { value := key; value.WindowSince = "2026-08-03T00:00:00Z"; return value }(),
	} {
		if _, found, err := reopened.GetSessionCostCache(changed); err != nil || found {
			t.Fatalf("cache %s partition found=%v err=%v", name, found, err)
		}
	}
}
