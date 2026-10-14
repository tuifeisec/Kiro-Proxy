package router

import (
	"testing"

	"kiro-proxy/store"
)

func TestResolveConfiguredUsesKiroPriorityWithoutFallback(t *testing.T) {
	r := &Router{
		models: []store.Model{
			{ID: "first", Name: "First", ModelID: "shared", Enabled: true},
			{ID: "second", Name: "Second", ModelID: "shared", Enabled: true},
			{ID: "disabled", Name: "Disabled", ModelID: "disabled-upstream"},
		},
		groups: []store.ModelGroup{{ID: "group", Name: "Group name", Enabled: true, Members: []store.ModelGroupMember{{ModelID: "second"}}}},
	}
	for _, tc := range []struct{ input, model, group string }{
		{"first", "first", ""}, {"second", "second", ""}, {"Second", "second", ""}, {"shared", "first", ""},
		{"group", "second", "group"}, {"Group name", "second", "group"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			route := r.ResolveConfigured(tc.input)
			if route == nil || route.Model == nil || route.Model.ID != tc.model {
				t.Fatalf("unexpected configured route: %+v", route)
			}
			if tc.group == "" && route.Group != nil || tc.group != "" && (route.Group == nil || route.Group.ID != tc.group) {
				t.Fatalf("unexpected group: %+v", route.Group)
			}
		})
	}
	for _, input := range []string{"unknown", "disabled", "disabled-upstream", "auto", "AUTO", "", " "} {
		if route := r.ResolveConfigured(input); route != nil {
			t.Fatalf("%q unexpectedly resolved: %+v", input, route)
		}
	}
	r.groups = append(r.groups, store.ModelGroup{ID: "empty-group", Name: "Empty group", Enabled: true})
	for _, input := range []string{"first", "Second", "shared", "group", "Group name", "empty-group", "Empty group", "disabled", "disabled-upstream"} {
		if !r.HasConfiguredTarget(input) {
			t.Fatalf("configured target %q was not recognized", input)
		}
	}
	for _, input := range []string{"unknown", "auto", "AUTO", "", " "} {
		if r.HasConfiguredTarget(input) {
			t.Fatalf("non-configured target %q was recognized", input)
		}
	}
	if route := r.ResolveConfigured("empty-group"); route != nil {
		t.Fatalf("empty group resolved: %+v", route)
	}
	if route := r.Resolve("unknown"); route == nil || route.Model.ID != "first" {
		t.Fatalf("existing Resolve fallback changed: %+v", route)
	}
}
