package router

import (
	"kiro-proxy/store"
	"path/filepath"
	"testing"
)

func routerTestModel(id, name string) store.Model {
	return store.Model{
		ID:       id,
		Name:     name,
		ModelID:  "upstream-" + id,
		Provider: "openai",
		BaseURL:  "https://example.invalid",
		Endpoint: "/v1/chat/completions",
		Enabled:  true,
	}
}

func TestResolveReturnsModelSnapshot(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "models.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	model := routerTestModel("model-a", "A")
	if err := db.CreateModel(&model); err != nil {
		t.Fatal(err)
	}
	r := New(db)
	first := r.Resolve(model.ID)
	if first == nil || first.Model == nil {
		t.Fatal("Resolve returned no route")
	}
	first.Model.APIKey = "mutated-route"
	first.Candidates[0].Name = "mutated-candidate"
	second := r.Resolve(model.ID)
	if second == nil || second.Model.APIKey == "mutated-route" || second.Model.Name != "A" {
		t.Fatalf("router storage was exposed through route: %#v", second)
	}
}

func TestGroupRouteReturnsCandidateSnapshots(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "models.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a := routerTestModel("model-a", "A")
	b := routerTestModel("model-b", "B")
	if err := db.CreateModel(&a); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateModel(&b); err != nil {
		t.Fatal(err)
	}
	group := store.ModelGroup{ID: "group-a", Name: "Group A", Enabled: true, Members: []store.ModelGroupMember{
		{ModelID: a.ID}, {ModelID: b.ID},
	}}
	if err := db.CreateModelGroup(&group); err != nil {
		t.Fatal(err)
	}
	r := New(db)
	first := r.Resolve(group.ID)
	if first == nil || len(first.Candidates) != 2 || len(first.Stages) != 1 {
		t.Fatalf("unexpected group route: %#v", first)
	}
	first.Candidates[0].APIKey = "mutated"
	first.Stages[0].Candidates[1].Name = "mutated-stage"
	second := r.Resolve(group.ID)
	if second == nil || second.Candidates[0].APIKey == "mutated" || second.Candidates[1].Name != "B" {
		t.Fatalf("group route exposed router storage: %#v", second)
	}
}
