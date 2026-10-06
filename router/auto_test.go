package router

import (
	"kiro-proxy/store"
	"path/filepath"
	"testing"
)

func newAutoTestRouter(t *testing.T, models []store.Model, groups []store.ModelGroup) *Router {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "models.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for i := range models {
		model := models[i]
		if err := db.CreateModel(&model); err != nil {
			t.Fatal(err)
		}
	}
	for i := range groups {
		group := groups[i]
		if err := db.CreateModelGroup(&group); err != nil {
			t.Fatal(err)
		}
	}
	return New(db)
}

func autoTestGroup(id, name string, modelIDs ...string) store.ModelGroup {
	group := store.ModelGroup{ID: id, Name: name, Enabled: true}
	for _, id := range modelIDs {
		group.Members = append(group.Members, store.ModelGroupMember{ModelID: id})
	}
	return group
}

func TestResolveAutoPrefersExplicitGroupAndPreservesRequestedName(t *testing.T) {
	auto := routerTestModel("model-auto", "Auto")
	groupModel := routerTestModel("model-group", "Group member")
	defaultModel := routerTestModel("model-default", "Default member")
	r := newAutoTestRouter(t, []store.Model{auto, groupModel, defaultModel}, []store.ModelGroup{
		autoTestGroup("group-auto", "aUtO", groupModel.ID),
		autoTestGroup("group-default", "Default", defaultModel.ID),
	})

	route := r.Resolve("AUTO")
	if route == nil || route.Group == nil || route.Group.ID != "group-auto" {
		t.Fatalf("Auto group did not take precedence: %#v", route)
	}
	if route.RequestedName != "AUTO" || route.Model.ID != groupModel.ID {
		t.Fatalf("unexpected Auto route: requested=%q model=%q", route.RequestedName, route.Model.ID)
	}
}

func TestResolveAutoPrefersExplicitModelThenDefaultGroup(t *testing.T) {
	auto := routerTestModel("model-auto", "aUtO")
	defaultModel := routerTestModel("model-default", "Default member")
	r := newAutoTestRouter(t, []store.Model{auto, defaultModel}, []store.ModelGroup{
		autoTestGroup("group-default", "Default", defaultModel.ID),
	})

	route := r.Resolve("auto")
	if route == nil || route.Model.ID != auto.ID || route.Group != nil {
		t.Fatalf("explicit Auto model did not take precedence: %#v", route)
	}

	r = newAutoTestRouter(t, []store.Model{defaultModel}, []store.ModelGroup{
		autoTestGroup("group-default", "Default", defaultModel.ID),
	})
	route = r.Resolve("auto")
	if route == nil || route.Group == nil || route.Group.ID != "group-default" {
		t.Fatalf("default group should precede a non-Auto model: %#v", route)
	}
}

func TestResolveAutoSkipsEmptyOrDisabledCandidates(t *testing.T) {
	disabled := routerTestModel("model-disabled", "Disabled")
	disabled.Enabled = false
	defaultModel := routerTestModel("model-default", "Default")
	r := newAutoTestRouter(t, []store.Model{disabled, defaultModel}, []store.ModelGroup{
		autoTestGroup("group-empty", "Auto"),
		autoTestGroup("group-disabled", "Disabled candidate", disabled.ID),
		autoTestGroup("group-default", "Default", defaultModel.ID),
	})

	route := r.Resolve("auto")
	if route == nil || route.Group == nil || route.Group.ID != "group-default" || route.Model.ID != defaultModel.ID {
		t.Fatalf("Auto did not skip groups without usable candidates: %#v", route)
	}
}

func TestResolveAutoWithoutTargetReturnsNil(t *testing.T) {
	r := newAutoTestRouter(t, nil, []store.ModelGroup{autoTestGroup("group-empty", "Auto")})
	if route := r.Resolve("auto"); route != nil {
		t.Fatalf("expected no Auto route, got %#v", route)
	}
	if model := r.AutoModel(); model != nil {
		t.Fatalf("expected no Auto model metadata, got %#v", model)
	}
}

func TestResolveByNameAutoFollowsSameRouteAndAutoModelMatchesTarget(t *testing.T) {
	a := routerTestModel("model-a", "A")
	b := routerTestModel("model-b", "B")
	r := newAutoTestRouter(t, []store.Model{a, b}, []store.ModelGroup{
		autoTestGroup("group-auto", "Auto", a.ID, b.ID),
	})

	main := r.Resolve("auto")
	classifier := r.ResolveByName("auto")
	if main == nil || classifier == nil || classifier.Group == nil || classifier.Group.ID != main.Group.ID {
		t.Fatalf("classifier Auto did not follow main Auto target: main=%#v classifier=%#v", main, classifier)
	}
	if classifier.RequestedName != "auto" {
		t.Fatalf("classifier lost requested name: %q", classifier.RequestedName)
	}

	metadata := r.AutoModel()
	if metadata == nil || metadata.ID != "auto" || metadata.Name != "Auto（默认路由）" || metadata.Note != "Auto（默认路由）" || metadata.Provider != "group" {
		t.Fatalf("unexpected local Auto metadata: %#v", metadata)
	}
}

func TestResolveExactUnknownModelDoesNotUseAutoFallback(t *testing.T) {
	model := routerTestModel("model-a", "A")
	r := newAutoTestRouter(t, []store.Model{model}, nil)
	if route := r.ResolveExact("unknown-model"); route != nil {
		t.Fatalf("ResolveExact unexpectedly fell back: %#v", route)
	}
}

func TestResolveAutoReturnsRecoverableSnapshots(t *testing.T) {
	model := routerTestModel("model-a", "A")
	model.APIKey = "original-key"
	r := newAutoTestRouter(t, []store.Model{model}, []store.ModelGroup{
		autoTestGroup("group-auto", "Auto", model.ID),
	})

	first := r.Resolve("auto")
	first.Model.APIKey = "mutated-route"
	first.Candidates[0].Name = "mutated-candidate"
	metadata := r.AutoModel()
	metadata.APIKey = "mutated-metadata"

	second := r.Resolve("auto")
	if second == nil || second.Model.APIKey != "original-key" || second.Model.Name != "A" {
		t.Fatalf("Auto route exposed router storage: %#v", second)
	}
	if next := r.AutoModel(); next == nil || next.APIKey != "" || next.Name != "Auto（默认路由）" {
		t.Fatalf("Auto metadata exposed mutable router data: %#v", next)
	}
}
