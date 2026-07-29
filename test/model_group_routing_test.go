package test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"kiro-proxy/proxy"
	"kiro-proxy/router"
	"kiro-proxy/store"
)

func TestModelGroupPersistenceAndRouting(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "model-groups.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	primary := createRoutingModel(t, db, "Primary", "model-primary")
	backup := createRoutingModel(t, db, "Backup", "model-backup")
	standalone := createRoutingModel(t, db, "Standalone", "model-standalone")

	group := &store.ModelGroup{
		Name:    "Production Group",
		Enabled: true,
		Members: []store.ModelGroupMember{
			{ModelID: backup.ID, Priority: 50},
			{ModelID: primary.ID, Priority: 10},
		},
	}
	if err := db.CreateModelGroup(group); err != nil {
		t.Fatal(err)
	}

	groups, err := db.GetModelGroups()
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || len(groups[0].Members) != 2 {
		t.Fatalf("unexpected persisted groups: %+v", groups)
	}
	if groups[0].Members[0].ModelID != backup.ID || groups[0].Members[0].Priority != 0 ||
		groups[0].Members[1].ModelID != primary.ID || groups[0].Members[1].Priority != 1 {
		t.Fatalf("member order was not normalized: %+v", groups[0].Members)
	}

	routes := router.New(db)
	groupRoute := routes.ResolveExact(group.Name)
	if groupRoute == nil || groupRoute.Group == nil || len(groupRoute.Candidates) != 2 {
		t.Fatalf("group route was not expanded: %+v", groupRoute)
	}
	if groupRoute.Candidates[0].ID != backup.ID || groupRoute.Candidates[1].ID != primary.ID {
		t.Fatalf("candidate priority mismatch: %+v", groupRoute.Candidates)
	}

	enabled := routes.EnabledModels()
	if len(enabled) != 4 {
		t.Fatalf("expected one logical group and all three individual models, got %+v", enabled)
	}
	visible := map[string]bool{}
	for _, model := range enabled {
		visible[model.Name] = true
	}
	if !visible[group.Name] || !visible[standalone.Name] || !visible[primary.Name] || !visible[backup.Name] {
		t.Fatalf("unexpected visible model list: %+v", visible)
	}

	memberRoute := routes.ResolveExact(primary.Name)
	if memberRoute == nil || memberRoute.Group != nil || len(memberRoute.Candidates) != 1 || memberRoute.Model.ID != primary.ID {
		t.Fatalf("group member did not preserve its individual route: %+v", memberRoute)
	}

	classifierRoute := routes.ResolveByID(primary.ID)
	if classifierRoute == nil || classifierRoute.Group != nil || len(classifierRoute.Candidates) != 1 || classifierRoute.Model.ID != primary.ID {
		t.Fatalf("internal model ID did not resolve exactly: %+v", classifierRoute)
	}

	if err := db.DeleteModel(backup.ID); err != nil {
		t.Fatal(err)
	}
	updatedGroup, err := db.GetModelGroup(group.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(updatedGroup.Members) != 1 || updatedGroup.Members[0].ModelID != primary.ID {
		t.Fatalf("deleted model membership was not cascaded: %+v", updatedGroup.Members)
	}
}

func TestModelGroupAdvertisesCombinedImageCapability(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "model-group-image-capability.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	textOnly := createRoutingModel(t, db, "Text only", "text-only")
	textOnly.ImageInputMode = store.ImageInputUnsupported
	if err := db.UpdateModel(textOnly); err != nil {
		t.Fatal(err)
	}
	vision := createRoutingModel(t, db, "Vision", "vision")
	vision.ImageInputMode = store.ImageInputSupported
	if err := db.UpdateModel(vision); err != nil {
		t.Fatal(err)
	}

	group := &store.ModelGroup{
		Name: "Mixed capability", Enabled: true,
		Members: []store.ModelGroupMember{{ModelID: textOnly.ID}, {ModelID: vision.ID}},
	}
	if err := db.CreateModelGroup(group); err != nil {
		t.Fatal(err)
	}

	models := router.New(db).EnabledModels()
	for _, model := range models {
		if model.Name == group.Name {
			if model.ImageInputMode != store.ImageInputSupported {
				t.Fatalf("group image mode = %q, want supported", model.ImageInputMode)
			}
			return
		}
	}
	t.Fatalf("group %q was not advertised", group.Name)
}

func TestModelGroupAdvancedDispatchConfiguration(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "advanced-model-groups.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	primaryModel := createRoutingModel(t, db, "Primary Account", "primary-account")
	fallbackModel := createRoutingModel(t, db, "Fallback Account", "fallback-account")
	fallback := &store.ModelGroup{
		Name:               "Fallback Group",
		Enabled:            true,
		FirstByteTimeoutMS: 15000,
		MaxAttempts:        2,
		Members: []store.ModelGroupMember{{
			ModelID: fallbackModel.ID, MaxConcurrency: 1,
		}},
	}
	if err := db.CreateModelGroup(fallback); err != nil {
		t.Fatal(err)
	}
	primary := &store.ModelGroup{
		Name:               "Primary Group",
		Description:        "Primary routing pool",
		Enabled:            true,
		FirstByteTimeoutMS: 12000,
		MaxAttempts:        3,
		FallbackGroupID:    fallback.ID,
		Members: []store.ModelGroupMember{{
			ModelID: primaryModel.ID, MaxConcurrency: 2,
		}},
	}
	if err := db.CreateModelGroup(primary); err != nil {
		t.Fatal(err)
	}

	persisted, err := db.GetModelGroup(primary.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Description != "Primary routing pool" || persisted.FirstByteTimeoutMS != 12000 || persisted.MaxAttempts != 3 || persisted.FallbackGroupID != fallback.ID {
		t.Fatalf("advanced group settings were not persisted: %+v", persisted)
	}
	if len(persisted.Members) != 1 || persisted.Members[0].MaxConcurrency != 2 {
		t.Fatalf("member concurrency was not persisted: %+v", persisted.Members)
	}

	route := router.New(db).ResolveExact(primary.Name)
	if route == nil || len(route.Candidates) != 2 {
		t.Fatalf("fallback candidates were not expanded: %+v", route)
	}
	if route.Candidates[0].ID != primaryModel.ID || route.Candidates[1].ID != fallbackModel.ID {
		t.Fatalf("fallback candidate order mismatch: %+v", route.Candidates)
	}
	if route.CandidateLimits[primaryModel.ID] != 2 || route.CandidateLimits[fallbackModel.ID] != 1 {
		t.Fatalf("candidate concurrency limits mismatch: %+v", route.CandidateLimits)
	}
	if route.CandidateGroups[primaryModel.ID] == nil || route.CandidateGroups[primaryModel.ID].ID != primary.ID {
		t.Fatalf("primary candidate lost its group configuration: %+v", route.CandidateGroups)
	}
	if route.CandidateGroups[fallbackModel.ID] == nil || route.CandidateGroups[fallbackModel.ID].ID != fallback.ID {
		t.Fatalf("fallback candidate lost its group configuration: %+v", route.CandidateGroups)
	}

	fallback.FallbackGroupID = primary.ID
	if err := db.UpdateModelGroup(fallback); err == nil {
		t.Fatal("expected fallback cycle validation error")
	}

	if err := db.DeleteModelGroup(fallback.ID); err != nil {
		t.Fatal(err)
	}
	persisted, err = db.GetModelGroup(primary.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.FallbackGroupID != "" {
		t.Fatalf("deleted fallback group reference was not cleared: %+v", persisted)
	}
}

func TestModelGroupRejectsAggressiveFirstByteTimeout(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "model-group-timeout.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	model := createRoutingModel(t, db, "Primary", "model-primary")
	group := &store.ModelGroup{
		Name:               "Aggressive Timeout",
		Enabled:            true,
		FirstByteTimeoutMS: 3000,
		Members:            []store.ModelGroupMember{{ModelID: model.ID}},
	}
	if err := db.CreateModelGroup(group); err == nil {
		t.Fatal("expected first byte timeout validation error")
	}
}

func TestModelGroupStatusTracksFirstByteAndConcurrency(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(10 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	db, err := store.Open(filepath.Join(t.TempDir(), "model-group-status.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	model := &store.Model{
		Name: "Observed Account", ModelID: "observed-account", Provider: "openai",
		BaseURL: upstream.URL, APIKey: "test-key", Endpoint: "/v1/responses", Enabled: true,
	}
	if err := db.CreateModel(model); err != nil {
		t.Fatal(err)
	}
	group := &store.ModelGroup{
		Name: "Observed Group", Enabled: true, FirstByteTimeoutMS: 10000,
		Members: []store.ModelGroupMember{{ModelID: model.ID, MaxConcurrency: 2}},
	}
	if err := db.CreateModelGroup(group); err != nil {
		t.Fatal(err)
	}

	route := router.New(db).ResolveExact(group.Name)
	forwarder := proxy.NewForwarder(store.NewStats(db), db)
	logger, err := proxy.NewTrafficLogger("", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	request := httptest.NewRequest(http.MethodPost, "http://proxy.test/v1/responses", nil)
	response, err := forwarder.Forward(request, &proxy.Preflight{
		Body: []byte(`{"model":"Observed Group","input":"hello"}`), Protocol: "responses",
	}, route, logger)
	if err != nil {
		t.Fatal(err)
	}

	status := modelStatusByID(t, forwarder.ModelHealthStatuses(), model.ID)
	if status.FirstByteTimeoutMS != 10000 || status.FirstByteSampleCount != 1 {
		t.Fatalf("unexpected first-byte status: %+v", status)
	}
	if status.LastFirstByteMS < 1 || status.AverageFirstByteMS != status.LastFirstByteMS {
		t.Fatalf("unexpected first-byte metrics: %+v", status)
	}
	if status.EWMAFirstByteMS != status.LastFirstByteMS || status.P95FirstByteMS != status.LastFirstByteMS || status.UpstreamBucketState != "closed" {
		t.Fatalf("unexpected recent latency or upstream status: %+v", status)
	}
	if status.CurrentConcurrency != 1 || status.ActiveRequests != 1 || status.MaxConcurrency != 2 {
		t.Fatalf("unexpected active concurrency status: %+v", status)
	}

	_, _ = io.Copy(io.Discard, response.Body)
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	status = modelStatusByID(t, forwarder.ModelHealthStatuses(), model.ID)
	if status.CurrentConcurrency != 0 || status.ActiveRequests != 0 {
		t.Fatalf("concurrency was not released after response close: %+v", status)
	}
}

func modelStatusByID(t *testing.T, statuses []store.ModelHealthStatus, modelID string) store.ModelHealthStatus {
	t.Helper()
	for _, status := range statuses {
		if status.ModelConfigID == modelID {
			return status
		}
	}
	t.Fatalf("model status not found: %s", modelID)
	return store.ModelHealthStatus{}
}

func createRoutingModel(t *testing.T, db *store.DB, name, modelID string) *store.Model {
	t.Helper()
	model := &store.Model{
		Name:     name,
		ModelID:  modelID,
		Provider: "openai",
		BaseURL:  "https://example.test",
		APIKey:   "test-key",
		Endpoint: "/v1/responses",
		Enabled:  true,
	}
	if err := db.CreateModel(model); err != nil {
		t.Fatal(err)
	}
	return model
}
