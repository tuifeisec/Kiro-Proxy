package router

import (
	"kiro-proxy/converter"
	"kiro-proxy/store"
	"testing"
)

// activityPathConversation builds a conversation whose workflow metadata is
// synthesized from the activity source: metadata version 2 with a node path but
// no definition identity, revision, or task type.
func activityPathConversation(nodePath string) *converter.ParsedConversation {
	conv := autoPolicyConversation()
	conv.WorkflowMetadataVersion = 2
	conv.WorkflowNodePath = nodePath
	return conv
}

// nodePathPolicyConfig extends the exact-identity duty fixture with entries
// whose node paths come from the activity source. These legacy full identities
// exercise compatibility with rules saved before path-only identities were supported.
func nodePathPolicyConfig() store.AutoRoutingConfig {
	config := autoDutyPolicyConfig()
	config.NodeOverrides = append(config.NodeOverrides,
		store.AutoNodeOverride{DefinitionID: "activity", DefinitionRevision: "rev-9", NodePath: "wf-abc/investigate", ModelID: "vision", ReasoningEffort: "high"},
		store.AutoNodeOverride{DefinitionID: "activity", DefinitionRevision: "rev-9", NodePath: "shared/path", ModelID: "vision", ReasoningEffort: "low"},
		store.AutoNodeOverride{DefinitionID: "activity-2", DefinitionRevision: "rev-9", NodePath: "shared/path", ModelID: "replacement", ReasoningEffort: "max"})
	config.NodeBindings = append(config.NodeBindings,
		store.AutoNodeBinding{DefinitionID: "activity", DefinitionRevision: "rev-9", NodePath: "wf-abc/plan", Duty: "design"},
		store.AutoNodeBinding{DefinitionID: "activity", DefinitionRevision: "rev-9", NodePath: "bound/path", Duty: "investigation"},
		store.AutoNodeBinding{DefinitionID: "activity-2", DefinitionRevision: "rev-9", NodePath: "bound/path", Duty: "design"})
	return config
}

func TestDutyFixedNodePathSecondaryMatchesSingleOverrideOrBinding(t *testing.T) {
	r := newAutoPolicyTestRouter(t, autoPolicyModels(), nodePathPolicyConfig())

	overrideHit := activityPathConversation("wf-abc/investigate")
	route, err := r.ResolveAuto(overrideHit)
	if err != nil || route.Model.ID != "vision" || route.AutoSelectionSource != "node_override_path" ||
		route.AutoReason != "node_override_path" || route.AutoEffort != "high" || !route.AutoEffortFixed ||
		route.AutoDuty != "" || route.AutoUnknownReason != "" {
		t.Fatalf("node path override hit = (%#v, %v)", route, err)
	}

	bindingHit := activityPathConversation("wf-abc/plan")
	route, err = r.ResolveAuto(bindingHit)
	if err != nil || route.Model.ID != "mode" || route.AutoSelectionSource != "node_binding_path" ||
		route.AutoReason != "node_binding_path:design" || route.AutoDuty != "design" ||
		route.AutoEffort != "medium" || !route.AutoEffortFixed {
		t.Fatalf("node path binding hit = (%#v, %v)", route, err)
	}
}

func TestDutyFixedNodePathAmbiguityFallsBackToDefault(t *testing.T) {
	for _, tc := range []struct {
		name     string
		nodePath string
	}{
		{name: "two overrides", nodePath: "shared/path"},
		{name: "two bindings", nodePath: "bound/path"},
		{name: "override and binding mixed", nodePath: "steps/investigate"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newAutoPolicyTestRouter(t, autoPolicyModels(), nodePathPolicyConfig())
			route, err := r.ResolveAuto(activityPathConversation(tc.nodePath))
			if err != nil || route.Model.ID != "default" || route.AutoSelectionSource != "default" ||
				route.AutoUnknownReason != "node_path_ambiguous" || route.AutoReason != "default:node_path_ambiguous" ||
				!route.AutoEffortFixed {
				t.Fatalf("ambiguous node path %q = (%#v, %v)", tc.nodePath, route, err)
			}
		})
	}
}

func TestDutyFixedNodePathUnmatchedKeepsExistingSelectionFlow(t *testing.T) {
	r := newAutoPolicyTestRouter(t, autoPolicyModels(), nodePathPolicyConfig())

	unmatched := activityPathConversation("wf-abc/unbound")
	route, err := r.ResolveAuto(unmatched)
	if err != nil || route.Model.ID != "default" || route.AutoSelectionSource != "default" ||
		route.AutoUnknownReason != "stable_identity_unavailable" || route.AutoReason != "default:stable_identity_unavailable" {
		t.Fatalf("unmatched node path changed the existing default = (%#v, %v)", route, err)
	}

	declared := activityPathConversation("wf-abc/unbound")
	declared.WorkflowTaskType = "design"
	route, err = r.ResolveAuto(declared)
	if err != nil || route.Model.ID != "mode" || route.AutoSelectionSource != "task_type" || route.AutoDuty != "design" {
		t.Fatalf("unmatched node path blocked the task type mapping = (%#v, %v)", route, err)
	}
}

func TestDutyFixedNodePathSecondarySkippedWhenDefinitionIdentityPresent(t *testing.T) {
	r := newAutoPolicyTestRouter(t, autoPolicyModels(), nodePathPolicyConfig())

	exactOverride := activityPathConversation("steps/investigate")
	exactOverride.WorkflowDefinitionID, exactOverride.WorkflowDefinitionRevision = "workflow", "rev-1"
	route, err := r.ResolveAuto(exactOverride)
	if err != nil || route.Model.ID != "replacement" || route.AutoSelectionSource != "node_override" ||
		route.AutoEffort != "max" || route.AutoDuty != "investigation" {
		t.Fatalf("exact override lost its priority to the secondary match = (%#v, %v)", route, err)
	}

	pathOnlyMatch := activityPathConversation("wf-abc/investigate")
	pathOnlyMatch.WorkflowDefinitionID, pathOnlyMatch.WorkflowDefinitionRevision = "workflow", "rev-1"
	route, err = r.ResolveAuto(pathOnlyMatch)
	if err != nil || route.Model.ID != "default" || route.AutoSelectionSource != "default" ||
		route.AutoUnknownReason != "task_type_unavailable" || route.AutoReason != "default:task_type_unavailable" {
		t.Fatalf("definition-backed conversation used the path-only secondary match = (%#v, %v)", route, err)
	}

	exactBinding := activityPathConversation("steps/other")
	exactBinding.WorkflowDefinitionID, exactBinding.WorkflowDefinitionRevision = "workflow", "rev-1"
	route, err = r.ResolveAuto(exactBinding)
	if err != nil || route.Model.ID != "mode" || route.AutoSelectionSource != "node_binding" || route.AutoDuty != "design" {
		t.Fatalf("exact binding lost its priority to the secondary match = (%#v, %v)", route, err)
	}
}

func TestDutyFixedNodePathSecondaryBeatsTaskTypeLikeExactIdentity(t *testing.T) {
	r := newAutoPolicyTestRouter(t, autoPolicyModels(), nodePathPolicyConfig())
	conv := activityPathConversation("wf-abc/investigate")
	conv.WorkflowTaskType = "design"
	route, err := r.ResolveAuto(conv)
	if err != nil || route.Model.ID != "vision" || route.AutoSelectionSource != "node_override_path" {
		t.Fatalf("node path match did not outrank the task type mapping = (%#v, %v)", route, err)
	}
}

func TestDutyFixedNodePathEmptyKeepsExistingDefault(t *testing.T) {
	r := newAutoPolicyTestRouter(t, autoPolicyModels(), nodePathPolicyConfig())
	route, err := r.ResolveAuto(activityPathConversation(""))
	if err != nil || route.Model.ID != "default" || route.AutoUnknownReason != "stable_identity_unavailable" {
		t.Fatalf("empty node path changed the existing default = (%#v, %v)", route, err)
	}
}
