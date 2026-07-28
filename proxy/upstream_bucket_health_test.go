package proxy

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"kiro-proxy/store"
)

func TestPrecommitFailureClassificationSeparatesProtocolAndTransport(t *testing.T) {
	protocolErr := fmt.Errorf("probe: %w", errSSEPrecommitUpstream)
	if !isUpstreamProtocolFailure(protocolErr) || isUpstreamTransportFailure(protocolErr) {
		t.Fatalf("explicit upstream failure classified incorrectly: %v", protocolErr)
	}

	missingEventErr := fmt.Errorf("probe: %w", errSSEPrecommitMissingEvent)
	if isUpstreamProtocolFailure(missingEventErr) || !isUpstreamTransportFailure(missingEventErr) {
		t.Fatalf("truncated stream classified incorrectly: %v", missingEventErr)
	}
}

func TestUpstreamBucketSingleModelFailureDoesNotOpen(t *testing.T) {
	health := testUpstreamBucketHealth(8)
	now := time.Unix(100, 0)
	model := testBucketModel("model-a", "https://API.Example.com:443/v1", "")

	health.recordFailure(model, now)
	if allowed, state := health.allow(model, now); !allowed || state != "closed" {
		t.Fatalf("single-model failure opened bucket: allowed=%v state=%s", allowed, state)
	}
}

func TestUpstreamBucketTwoModelsOpenBucket(t *testing.T) {
	health := testUpstreamBucketHealth(8)
	now := time.Unix(200, 0)
	first := testBucketModel("model-a", "https://api.example.com/v1", "")
	second := testBucketModel("model-b", "https://api.example.com/v2", "")

	health.recordFailure(first, now)
	health.recordFailure(second, now.Add(time.Second))
	if allowed, state := health.allow(first, now.Add(2*time.Second)); allowed || state != "open" {
		t.Fatalf("two-model failure did not open bucket: allowed=%v state=%s", allowed, state)
	}
}

func TestUpstreamBucketExpiredOpenAllowsSingleHalfOpenProbe(t *testing.T) {
	health := testUpstreamBucketHealth(8)
	now := time.Unix(300, 0)
	first := testBucketModel("model-a", "https://api.example.com", "")
	second := testBucketModel("model-b", "https://api.example.com", "")
	health.recordFailure(first, now)
	health.recordFailure(second, now)

	probeAt := now.Add(11 * time.Second)
	if allowed, state := health.allow(first, probeAt); !allowed || state != "half-open" {
		t.Fatalf("expired bucket did not allow half-open probe: allowed=%v state=%s", allowed, state)
	}
	if allowed, state := health.allow(second, probeAt); allowed || state != "half-open-busy" {
		t.Fatalf("parallel half-open probe was not blocked: allowed=%v state=%s", allowed, state)
	}
	if allowed, state := health.allow(second, probeAt.Add(6*time.Second)); !allowed || state != "half-open" {
		t.Fatalf("expired half-open lease was not reclaimed: allowed=%v state=%s", allowed, state)
	}
}

func TestUpstreamBucketSuccessRecoversBucket(t *testing.T) {
	health := testUpstreamBucketHealth(8)
	now := time.Unix(400, 0)
	first := testBucketModel("model-a", "https://api.example.com", "")
	second := testBucketModel("model-b", "https://api.example.com", "")
	health.recordFailure(first, now)
	health.recordFailure(second, now)
	health.recordSuccess(first, now.Add(time.Second))

	if allowed, state := health.allow(second, now.Add(time.Second)); !allowed || state != "closed" {
		t.Fatalf("success did not recover bucket: allowed=%v state=%s", allowed, state)
	}
}

func TestUpstreamBucketNeutralReleasesHalfOpenProbe(t *testing.T) {
	health := testUpstreamBucketHealth(8)
	now := time.Unix(450, 0)
	first := testBucketModel("model-a", "https://api.example.com", "")
	second := testBucketModel("model-b", "https://api.example.com", "")
	health.recordFailure(first, now)
	health.recordFailure(second, now)
	probeAt := now.Add(11 * time.Second)
	if allowed, _ := health.allow(first, probeAt); !allowed {
		t.Fatal("expected initial half-open probe")
	}
	health.neutral(first, probeAt)
	if allowed, state := health.allow(second, probeAt); !allowed || state != "half-open" {
		t.Fatalf("neutral probe did not release lease: allowed=%v state=%s", allowed, state)
	}
}

func TestUpstreamBucketDifferentOriginsAreIsolatedAndOrdered(t *testing.T) {
	health := testUpstreamBucketHealth(8)
	now := time.Unix(500, 0)
	first := testBucketModel("model-a", "https://api.example.com/v1", "http://proxy.example:80")
	second := testBucketModel("model-b", "https://api.example.com/v2", "http://proxy.example")
	other := testBucketModel("model-c", "https://other.example.com/v1", "http://proxy.example")
	health.recordFailure(first, now)
	health.recordFailure(second, now)

	if allowed, state := health.allow(other, now); !allowed || state != "closed" {
		t.Fatalf("different origin inherited bucket state: allowed=%v state=%s", allowed, state)
	}
	ordered := health.order([]*store.Model{first, other}, now)
	if len(ordered) != 2 || ordered[0].ID != other.ID || ordered[1].ID != first.ID {
		t.Fatalf("healthy origin was not promoted: %+v", ordered)
	}
	if upstreamBucketKey(first.BaseURL, first.ProxyURL) != upstreamBucketKey(second.BaseURL, second.ProxyURL) {
		t.Fatal("equivalent origins or proxy URLs were not normalized into one bucket")
	}
}

func TestUpstreamBucketCapacityEvictsOldestEntry(t *testing.T) {
	health := testUpstreamBucketHealth(2)
	now := time.Unix(600, 0)
	oldest := testBucketModel("model-a", "https://oldest.example.com", "")
	middle := testBucketModel("model-b", "https://middle.example.com", "")
	newest := testBucketModel("model-c", "https://newest.example.com", "")

	health.recordFailure(oldest, now)
	health.recordFailure(middle, now.Add(time.Second))
	health.recordFailure(newest, now.Add(2*time.Second))

	if len(health.buckets) != 2 {
		t.Fatalf("capacity was not enforced: %d", len(health.buckets))
	}
	if _, exists := health.buckets[upstreamBucketKeyForModel(oldest)]; exists {
		t.Fatal("oldest bucket was not evicted")
	}
	if _, exists := health.buckets[upstreamBucketKeyForModel(newest)]; !exists {
		t.Fatal("newest bucket was unexpectedly evicted")
	}
}

func TestUpstreamBucketExpiredEvidenceIsCleaned(t *testing.T) {
	health := testUpstreamBucketHealth(8)
	now := time.Unix(700, 0)
	model := testBucketModel("model-a", "https://api.example.com", "")
	health.recordFailure(model, now)

	health.order([]*store.Model{model, testBucketModel("model-b", "https://other.example.com", "")}, now.Add(6*time.Second))
	if len(health.buckets) != 0 {
		t.Fatalf("expired failure evidence was not cleaned: %d", len(health.buckets))
	}
}

func TestUpstreamBucketKeyDoesNotContainAPIKey(t *testing.T) {
	model := testBucketModel("model-a", "https://api.example.com/v1", "")
	model.APIKey = "secret-api-key"
	key := upstreamBucketKeyForModel(model)
	if strings.Contains(key, model.APIKey) {
		t.Fatal("bucket key contains API key")
	}
}

func testUpstreamBucketHealth(capacity int) *upstreamBucketHealth {
	return newUpstreamBucketHealthWithConfig(upstreamBucketHealthConfig{
		failureWindow: 5 * time.Second,
		openTTL:       10 * time.Second,
		halfOpenTTL:   5 * time.Second,
		idleTTL:       time.Minute,
		capacity:      capacity,
	})
}

func testBucketModel(id, baseURL, proxyURL string) *store.Model {
	return &store.Model{ID: id, BaseURL: baseURL, ProxyURL: proxyURL}
}
