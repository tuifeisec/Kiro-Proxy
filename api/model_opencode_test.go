package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"kiro-proxy/store"
)

func TestModelOpenCodeSessionAPIRoundTripAndRouterReload(t *testing.T) {
	db, server := newCredentialAPITestServer(t, nil)

	model := store.Model{
		ID:                    "api-opencode",
		Name:                  "API OpenCode",
		ModelID:               "api-opencode-model",
		Provider:              "openai",
		BaseURL:               "https://example.test",
		APIKey:                "api-key",
		Endpoint:              "/v1/chat/completions",
		EnableOpenCodeSession: true,
	}
	status, response, apiError := requestModelJSON(t, server.Handler(), http.MethodPost, "/api/models", model)
	if status != http.StatusCreated {
		t.Fatalf("POST status=%d error=%v response=%#v", status, apiError, response)
	}
	if !response.EnableOpenCodeSession {
		t.Fatal("POST response lost enable_opencode_session=true")
	}
	saved, err := db.GetModel(model.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !saved.EnableOpenCodeSession {
		t.Fatal("POST did not persist enable_opencode_session=true")
	}
	if route := server.router.Resolve(model.ID); route == nil || route.Model == nil || !route.Model.EnableOpenCodeSession {
		t.Fatal("router reload did not expose enable_opencode_session=true")
	}

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/models", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /api/models status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var models []store.Model
	if err := json.Unmarshal(recorder.Body.Bytes(), &models); err != nil {
		t.Fatalf("decode GET /api/models: %v", err)
	}
	if len(models) != 1 || models[0].ID != model.ID || !models[0].EnableOpenCodeSession {
		t.Fatalf("GET /api/models lost enable_opencode_session=true: %#v", models)
	}

	status, response, apiError = requestModelJSON(t, server.Handler(), http.MethodGet, "/api/models/"+model.ID, nil)
	if status != http.StatusOK {
		t.Fatalf("GET /api/models/:id status=%d error=%v response=%#v", status, apiError, response)
	}
	if !response.EnableOpenCodeSession {
		t.Fatal("GET /api/models/:id lost enable_opencode_session=true")
	}

	updated := response
	updated.EnableOpenCodeSession = false
	status, response, apiError = requestModelJSON(t, server.Handler(), http.MethodPut, "/api/models/"+model.ID, updated)
	if status != http.StatusOK {
		t.Fatalf("PUT status=%d error=%v response=%#v", status, apiError, response)
	}
	if response.EnableOpenCodeSession {
		t.Fatal("PUT response lost enable_opencode_session=false")
	}
	saved, err = db.GetModel(model.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.EnableOpenCodeSession {
		t.Fatal("PUT did not persist enable_opencode_session=false")
	}
	if route := server.router.Resolve(model.ID); route == nil || route.Model == nil || route.Model.EnableOpenCodeSession {
		t.Fatal("router reload did not expose enable_opencode_session=false")
	}

	status, response, apiError = requestModelJSON(t, server.Handler(), http.MethodGet, "/api/models/"+model.ID, nil)
	if status != http.StatusOK {
		t.Fatalf("final GET status=%d error=%v response=%#v", status, apiError, response)
	}
	if response.EnableOpenCodeSession {
		t.Fatal("final GET did not return enable_opencode_session=false")
	}
}
