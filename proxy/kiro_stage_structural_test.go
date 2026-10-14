package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func kiroStageRealOriginal(t *testing.T, env string) []byte {
	t.Helper()
	path := os.Getenv(env)
	if path == "" {
		t.Skip(env + " 未设置")
	}
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return source
}

func TestKiroStageStructuralRealVariants(t *testing.T) {
	for _, env := range []string{"KIRO_STAGE_TEST_BUNDLE", "KIRO_STAGE_TEST_BUNDLE_130"} {
		t.Run(env, func(t *testing.T) {
			original := kiroStageRealOriginal(t, env)
			analysis, err := analyzeKiroStageJS(original)
			if err != nil {
				t.Fatal(err)
			}
			// α 变体只重命名不参与 shorthand 的绑定，固定对象字段不改变。
			fixed := map[string]bool{}
			for _, token := range analysis.Tokens {
				if token.Key != "V" && token.Binding != "" {
					fixed[token.Binding] = true
				}
			}
			aliases := map[string]string{}
			var renamed []byte
			var alphaQuoted []byte
			previous := 0
			quotedPrevious := 0
			for _, token := range analysis.Tokens {
				if token.Key == "S:auto" || token.Key == "S:workflow.new.generated_cleanup_failed" {
					alphaQuoted = append(alphaQuoted, original[quotedPrevious:token.Start]...)
					alphaQuoted = append(alphaQuoted, "'"+strings.TrimPrefix(token.Key, "S:")+"'"...)
					quotedPrevious = token.End
				}
				if token.Key != "V" || fixed[token.Binding] {
					continue
				}
				name := fmt.Sprintf("kx%d", token.Declaration)
				aliases[token.Name+"@"+fmt.Sprint(token.Declaration)] = name
				renamed = append(renamed, original[previous:token.Start]...)
				renamed = append(renamed, name...)
				previous = token.End
				alphaQuoted = append(alphaQuoted, original[quotedPrevious:token.Start]...)
				alphaQuoted = append(alphaQuoted, name...)
				quotedPrevious = token.End
			}
			renamed = append(renamed, original[previous:]...)
			alphaQuoted = append(alphaQuoted, original[quotedPrevious:]...)
			var quoted []byte
			// decoded string 语义相同，不只是更新文件尾导致 hash 改变。
			previous = 0
			for _, token := range analysis.Tokens {
				if token.Key != "S:auto" && token.Key != "S:workflow.new.generated_cleanup_failed" {
					continue
				}
				quoted = append(quoted, original[previous:token.Start]...)
				quoted = append(quoted, "'"+strings.TrimPrefix(token.Key, "S:")+"'"...)
				previous = token.End
			}
			quoted = append(quoted, original[previous:]...)
			formatted := bytes.ReplaceAll(original, []byte("buildRequestCommand("), []byte("buildRequestCommand /* update formatting */ ("))
			combined := alphaQuoted
			combined = bytes.ReplaceAll(combined, []byte("buildRequestCommand("), []byte("buildRequestCommand /* update formatting */ ("))
			getter := "hoe"
			if strings.HasSuffix(env, "130") {
				getter = "yse"
			}
			var collision []byte
			previous = 0
			for _, token := range analysis.Tokens {
				if token.Name != getter || token.Binding == "" {
					continue
				}
				collision = append(collision, original[previous:token.Start]...)
				collision = append(collision, "serialized"...)
				previous = token.End
			}
			collision = append(collision, original[previous:]...)
			variants := []struct {
				name   string
				source []byte
			}{
				{"original", original},
				{"alpha", renamed},
				{"quotes", quoted},
				{"format", formatted},
				{"outside", append(append([]byte(nil), original...), []byte("\n;const kiroUpdateUnrelated=20261014;\n")...)},
				{"combined", append(combined, []byte("\n/* harmless update */\n")...)},
				{"collision", collision},
			}
			for _, variant := range variants {
				t.Run(variant.name, func(t *testing.T) {
					inputCopy := append([]byte(nil), variant.source...)
					candidate, err := buildKiroStageStructuralCandidate(variant.source)
					if err != nil {
						t.Fatal(err)
					}
					if !bytes.Equal(variant.source, inputCopy) {
						t.Fatal("编译器修改了输入")
					}
					restored, found, err := unwrapKiroStageStructuralCandidate(candidate)
					if err != nil || !found || !bytes.Equal(restored, variant.source) {
						t.Fatalf("恢复自检: %v", err)
					}
					if !bytes.Contains(candidate, []byte(kiroStageSHA256(variant.source))) {
						t.Fatal("桥接未绑定当前原版摘要")
					}
					if dir := os.Getenv("KIRO_STAGE_AUTO_TEST_OUTPUT"); dir != "" {
						family := "legacy"
						if strings.HasSuffix(env, "130") {
							family = "native"
						}
						if err := os.MkdirAll(dir, 0755); err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(filepath.Join(dir, family+"-"+variant.name+".js"), candidate, 0644); err != nil {
							t.Fatal(err)
						}
						if variant.name == "alpha" {
							data, _ := json.Marshal(aliases)
							if err := os.WriteFile(filepath.Join(dir, family+"-aliases.json"), data, 0644); err != nil {
								t.Fatal(err)
							}
						}
					}
				})
			}
		})
	}
}

func TestKiroStageStructuralRejectsInvalidRealInputs(t *testing.T) {
	original := kiroStageRealOriginal(t, "KIRO_STAGE_TEST_BUNDLE_130")
	manifest, err := readKiroStageManifest("kiro_stage_bridge_130_manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	variants := map[string][]byte{
		"missing":               bytes.Replace(original, []byte("workflow.new.generated_cleanup_failed"), []byte("workflow.new.removed_cleanup_failed"), 1),
		"syntax":                append(append([]byte(nil), original...), []byte("\nfunction broken(")...),
		"partial":               append(append([]byte(nil), original...), []byte("\nfunction kiroProxyPartialBridge(){}")...),
		"wrong-binding":         bytes.Replace(original, []byte("buildRequestCommand(l,a,e.additionalModelRequestFields,n,s)"), []byte("buildRequestCommand(l,a,l.additionalModelRequestFields,n,s)"), 1),
		"duplicate":             append(append([]byte(nil), original...), []byte("\n"+manifest.Patches[6].Before)...),
		"native-path-semantics": bytes.Replace(original, []byte("function u3(t,e){return mc.join(t,e)}"), []byte("function u3(t,e){return mc.join(t,e,\"changed\")}"), 1),
		"native-path-module":    bytes.Replace(original, []byte(`mc=q(require("node:path"),1)`), []byte(`mc=q(require("node:crypto"),1)`), 1),
	}
	for name, input := range variants {
		t.Run(name, func(t *testing.T) {
			before := append([]byte(nil), input...)
			if _, err := buildKiroStageStructuralCandidate(input); err == nil {
				t.Fatal("非法结构被接受")
			}
			if !bytes.Equal(before, input) {
				t.Fatal("失败修改了输入")
			}
		})
	}
}

func TestKiroStageContextBindingIdentity(t *testing.T) {
	pattern := []kiroStagePatternToken{{Key: "V", Binding: "a"}, {Key: "P:="}, {Key: "V", Binding: "b"}, {Key: "P:+"}, {Key: "V", Binding: "a"}}
	for _, item := range []struct {
		name     string
		bindings []string
		want     int
	}{
		{"rename", []string{"X", "", "Y", "", "X"}, 1},
		{"shadow", []string{"X", "", "Y", "", "Z"}, 0},
		{"collapse", []string{"X", "", "X", "", "X"}, 0},
	} {
		t.Run(item.name, func(t *testing.T) {
			tokens := make([]kiroStageJSToken, len(pattern))
			for i, p := range pattern {
				tokens[i] = kiroStageJSToken{Key: p.Key, Binding: item.bindings[i]}
			}
			if matches := findKiroStageContext(tokens, pattern); len(matches) != item.want {
				t.Fatalf("matches %v", matches)
			}
		})
	}
}

func TestKiroStageStructuralUpdateAndLegacyInstall(t *testing.T) {
	original := kiroStageRealOriginal(t, "KIRO_STAGE_TEST_BUNDLE_130")
	manifest, err := readKiroStageManifest("kiro_stage_bridge_130_manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := buildKiroStageCandidate(original, manifest)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	executable := filepath.Join(root, "Kiro.exe")
	target := kiroStageBundlePath(executable)
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, legacy, 0600); err != nil {
		t.Fatal(err)
	}
	cache := newStagePreflightTestCache(t, t.TempDir(), kiroStageSHA256([]byte("legacy-update-test")))
	plan, err := prepareKiroStageInstallWith(executable, cache, buildKiroStageStructuralCandidate)
	if err != nil {
		t.Fatal(err)
	}
	result, err := installKiroStageBridge(plan)
	if err != nil {
		t.Fatal(err)
	}
	backup, err := os.ReadFile(result.backupPath)
	if err != nil || !bytes.Equal(backup, legacy) {
		t.Fatalf("旧桥接完整备份: %v", err)
	}
	updated := append(append([]byte(nil), original...), []byte("\n/* simulated Kiro update, unregistered hash */\n")...)
	if err := os.WriteFile(target, updated, 0600); err != nil {
		t.Fatal(err)
	}
	newPlan, err := prepareKiroStageInstallWith(executable, cache, buildKiroStageStructuralCandidate)
	if err != nil {
		t.Fatal(err)
	}
	if newPlan.originalHash == manifest.OriginalSHA256 || newPlan.candidateHash == plan.candidateHash {
		t.Fatal("更新未重新适配")
	}
	newResult, err := installKiroStageBridge(newPlan)
	if err != nil {
		t.Fatal(err)
	}
	if newResult.backupPath == result.backupPath {
		t.Fatal("更新覆盖旧备份")
	}
	newBackup, err := os.ReadFile(newResult.backupPath)
	if err != nil || !bytes.Equal(newBackup, updated) {
		t.Fatalf("更新完整备份: %v", err)
	}
	installed, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	installed[0] ^= 1
	if err := os.WriteFile(target, installed, 0600); err != nil {
		t.Fatal(err)
	}
	entriesBefore, _ := os.ReadDir(filepath.Dir(target))
	if _, err := prepareKiroStageInstallWith(executable, cache, buildKiroStageStructuralCandidate); err == nil {
		t.Fatal("损坏capsule payload被接受")
	}
	entriesAfter, _ := os.ReadDir(filepath.Dir(target))
	current, _ := os.ReadFile(target)
	if !bytes.Equal(current, installed) || len(entriesBefore) != len(entriesAfter) {
		t.Fatal("失败预检写入安装目录")
	}
}
