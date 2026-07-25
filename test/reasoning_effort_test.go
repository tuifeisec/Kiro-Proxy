package test

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"kiro-proxy/converter"
	"kiro-proxy/store"
)

func TestKiroReasoningEffortOverride(t *testing.T) {
	request := map[string]interface{}{
		"conversationState": map[string]interface{}{
			"conversationId": "reasoning-test",
			"currentMessage": map[string]interface{}{
				"userInputMessage": map[string]interface{}{
					"modelId": "gpt-5.6-sol",
					"content": "hello",
				},
			},
		},
		"additionalModelRequestFields": map[string]string{"reasoningEffort": "LOW"},
	}
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	conv, err := converter.ParseKiroRequest(body)
	if err != nil {
		t.Fatalf("parse override: %v", err)
	}
	if conv.ReasoningEffort != "low" {
		t.Fatalf("reasoning effort = %q, want low", conv.ReasoningEffort)
	}

	request["additionalModelRequestFields"] = map[string]string{"reasoningEffort": "extreme"}
	body, _ = json.Marshal(request)
	if _, err := converter.ParseKiroRequest(body); err == nil {
		t.Fatal("unsupported reasoning effort should be rejected")
	}
}

func TestRequestUsageStoresReasoningEffort(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	err = db.InsertRequestUsage(&store.RequestUsage{
		ModelID:         "gpt-5.6-sol",
		Provider:        "openai",
		Endpoint:        "/responses",
		StatusCode:      200,
		Success:         true,
		ReasoningEffort: "medium",
	})
	if err != nil {
		t.Fatalf("insert usage: %v", err)
	}
	items, _, err := db.QueryRequestUsage("", "", 10)
	if err != nil {
		t.Fatalf("query usage: %v", err)
	}
	if len(items) != 1 || items[0].ReasoningEffort != "medium" {
		t.Fatalf("stored reasoning effort = %+v", items)
	}
}
