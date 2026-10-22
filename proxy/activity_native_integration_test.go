package proxy

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kiro-proxy/store"
)

// 使用官方批量 envelope 和原生 continuation，不注入代理私有字段。
func nativeActivityEnvelope(session string, seq int, node string) string {
	return fmt.Sprintf(`{"payload":[{"sessionId":%q,"seq":%d,"activityTimestamp":"2026-10-22T08:00:00Z","messageId":"event-%d","agentMode":"vibe","content":{"executionId":"observed-execution","_meta":{"kiro":{"agentMode":"vibe","workflow":{"workflowId":"wf-native","nodeId":%q,"nodePath":["wf-native",%q]}}}}}]}`, session, seq, seq, node, node)
}

func nativeActivityRawRequest(model, session, continuation string) *http.Request {
	body := fmt.Sprintf(`{"model":%q,"stream":true,"input":[{"type":"function_call","call_id":"historical-call","name":"read_file","arguments":"{}"},{"type":"function_call_output","call_id":"historical-call","output":"old result"},{"role":"user","content":"new task"}],"metadata":{"kiro:conversation_id":%q,"kiro:root_conversation_id":%q,"kiro:agent_mode":"vibe","kiro:agent_continuation_id":%q}}`, model, session, session, continuation)
	return httptest.NewRequest(http.MethodPost, "http://runtime.us-east-1.kiro.dev/v1/responses", strings.NewReader(body))
}

func TestActivityNativeContinuationDoesNotChangeModelOnLateActivity(t *testing.T) {
	oldMode := GetProxyMode()
	SetProxyMode(ModeForward)
	t.Cleanup(func() { SetProxyMode(oldMode) })
	env := newAutoStageIntegrationEnv(t)
	rawStageProxy(t, env, nil)
	session := "sess_native-late-activity"
	sendAuto := func(continuation, want string) {
		t.Helper()
		resp := serveRawStage(t, env, nativeActivityRawRequest("auto", session, continuation))
		if resp.Code != http.StatusOK {
			t.Fatalf("native auto status=%d body=%s", resp.Code, resp.Body.String())
		}
		if got := <-env.requests; got.Model != want {
			t.Fatalf("continuation=%s model=%s, want %s", continuation, got.Model, want)
		}
	}
	sendActivity := func(seq int, node string) {
		t.Helper()
		resp := serveRawStage(t, env, httptest.NewRequest(http.MethodPost,
			"http://runtime.us-east-1.kiro.dev/agents/activity", strings.NewReader(nativeActivityEnvelope(session, seq, node))))
		if resp.Code != http.StatusNoContent || resp.Body.Len() != 0 {
			t.Fatalf("activity status=%d body=%s", resp.Code, resp.Body.String())
		}
	}

	sendAuto("generation-1", "upstream-default")
	sendActivity(1, "plan")
	sendAuto("generation-1", "upstream-default")
	sendAuto("generation-2", "upstream-shared-responses")
	sendActivity(3, "implement")
	sendActivity(2, "finalize") // 较旧主流事件不得回滚观察。
	sendAuto("generation-2", "upstream-shared-responses")
	sendAuto("generation-3", "upstream-implement")

	// 同一原生分支手动选择后，下一次 Auto 应重新初选，不能保留旧快照。
	sendActivity(4, "finalize")
	manual := serveRawStage(t, env, nativeActivityRawRequest("manual", session, "generation-3"))
	if manual.Code != http.StatusOK {
		t.Fatalf("manual status=%d body=%s", manual.Code, manual.Body.String())
	}
	if got := <-env.requests; got.Model != "upstream-manual" {
		t.Fatalf("manual model=%s", got.Model)
	}
	sendAuto("generation-3", "upstream-finalize")
}

func TestActivityNativePathRulePersistsAndRoutesWithObservedProvenance(t *testing.T) {
	oldMode := GetProxyMode()
	SetProxyMode(ModeForward)
	t.Cleanup(func() { SetProxyMode(oldMode) })
	env := newAutoStageIntegrationEnv(t)
	config := store.AutoRoutingConfig{
		Version: store.AutoRoutingVersion, Mode: store.AutoRoutingModeDutyFixed,
		Layout: store.AutoRoutingLayoutStandard, Enabled: true, DefaultModelID: "default",
		DutyModels:    dutyModelsForIntegrationTest(),
		NodeOverrides: []store.AutoNodeOverride{{NodePath: "plan", ModelID: "implement", ReasoningEffort: "high"}},
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := store.ParseAutoRoutingConfig(string(encoded))
	if err != nil {
		t.Fatalf("path-only configuration cannot be persisted: %v", err)
	}
	setAutoStageIntegrationConfig(t, env.db, parsed)
	env.server.router.Reload()
	rawStageProxy(t, env, nil)
	session := "sess_native-path-rule"
	activity := serveRawStage(t, env, httptest.NewRequest(http.MethodPost,
		"http://runtime.us-east-1.kiro.dev/agents/activity", strings.NewReader(nativeActivityEnvelope(session, 5, "plan"))))
	if activity.Code != http.StatusNoContent {
		t.Fatalf("activity status=%d", activity.Code)
	}
	resp := serveRawStage(t, env, nativeActivityRawRequest("auto", session, "generation-path"))
	if resp.Code != http.StatusOK {
		t.Fatalf("auto status=%d body=%s", resp.Code, resp.Body.String())
	}
	if got := <-env.requests; got.Model != "upstream-implement" {
		t.Fatalf("path-only rule routed to %s", got.Model)
	}
	observations := env.server.router.GetAutoObservations()
	if len(observations) != 1 || observations[0].WorkflowStageSource != "activity_observation" {
		t.Fatalf("asynchronous provenance was not preserved: %+v", observations)
	}
}
