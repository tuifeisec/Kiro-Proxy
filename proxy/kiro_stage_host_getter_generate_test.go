package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const (
	kiroStageHostGetterGenerateEnv = "KIRO_STAGE_HOST_GETTER_GENERATE"
	kiroStageHostGetterOutputEnv   = "KIRO_STAGE_HOST_GETTER_OUTPUT_DIR"
	kiroStageHostGetterPatchName   = "workflow-host-route-accessor"
	kiroStageHostGetterBefore      = `getSessionModelId:c(w=>this.host.getSession(w)?.modelId,"getSessionModelId")`
	kiroStageHostGetterProperty    = `getSessionKiroProxyRoute:c(kiroProxySessionIdV3=>this.host.getSession(kiroProxySessionIdV3)?.kiroProxyRoute,"getSessionKiroProxyRoute")`
	kiroStageHostGetterOldCall     = `t.host.getSession(p)?.kiroProxyRoute`
	kiroStageHostGetterNewCall     = `t.host.getSessionKiroProxyRoute(p)`
	kiroStageHostGetterOldAdapter  = "3cfca7cb7f4808d82bb54141b203ab4900852afc86b1f808ac43e6a38259445d"
	kiroStageHostGetterLegacyHash  = "6acca01dfa75f7a301618f59736c996b481843445838bf618499487194d35fbb"
	kiroStageHostGetterNativeHash  = "da956c67134e9de239795ac6bbd3c05f9312516d1685b45e217bc8d2826b9bb7"
)

// This generator is intentionally opt-in. Ordinary go test runs never rewrite
// the embedded profiles or either bridge manifest.
func TestGenerateKiroStageHostGetterArtifacts(t *testing.T) {
	if os.Getenv(kiroStageHostGetterGenerateEnv) != "1" {
		t.Skip("set " + kiroStageHostGetterGenerateEnv + "=1 to regenerate checked-in Kiro bridge artifacts")
	}

	repoRoot, err := filepath.Abs(filepath.Join(".."))
	if err != nil {
		t.Fatal(err)
	}
	outputDir := os.Getenv(kiroStageHostGetterOutputEnv)
	if outputDir == "" {
		t.Fatal(kiroStageHostGetterOutputEnv + " must name a new directory under the OS temporary directory")
	}
	outputDir, err = filepath.Abs(outputDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := requireNewTemporaryDirectory(outputDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(outputDir, 0700); err != nil {
		t.Fatal(err)
	}

	profiles, previousProfiles, profilesPath, err := loadHostGetterProfiles(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	nodePath, err := exec.LookPath("node")
	if err != nil {
		nodePath, err = exec.LookPath("node.exe")
		if err != nil {
			t.Fatalf("Node.js is required for the JavaScript syntax checks: %v", err)
		}
	}
	if err := runNodeCheck(nodePath, repoRoot, filepath.Join(repoRoot, "tools", "kiro_stage_bridge.mjs")); err != nil {
		t.Fatal(err)
	}
	legacyPatches, err := readLegacyBridgePatches(nodePath, repoRoot)
	if err != nil {
		t.Fatal(err)
	}

	families := []struct {
		name, sample, expectedOriginal string
		wantHooks, wantEdits           int
	}{
		{"legacy-workflow", "legacy-original.js", kiroStageHostGetterLegacyHash, 35, 32},
		{"native-model-routing", "native-original.js", kiroStageHostGetterNativeHash, 34, 31},
	}
	inputs := make([]hostGetterFamilyInput, 0, len(families))
	for _, family := range families {
		input, err := loadHostGetterOriginal(repoRoot, outputDir, family.name, family.sample, family.expectedOriginal)
		if err != nil {
			t.Fatalf("load %s original: %v", family.name, err)
		}
		profile := findHostGetterProfile(profiles, family.name)
		analysis, err := analyzeKiroStageJS(input.original)
		if err != nil {
			t.Fatalf("analyze %s original: %v", family.name, err)
		}
		if err := appendHostGetterHook(profile, input.original, analysis); err != nil {
			t.Fatalf("append %s host getter hook: %v", family.name, err)
		}
		if profile == nil || len(profile.Hooks) != family.wantHooks {
			t.Fatalf("%s generated hook count mismatch", family.name)
		}
		if err := verifyPreservedProfileHooks(*findHostGetterProfile(previousProfiles, family.name), *profile); err != nil {
			t.Fatal(err)
		}
		inputs = append(inputs, input)
	}
	profileBytes, err := json.MarshalIndent(profiles, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	profileBytes = append(profileBytes, '\n')
	profileHash := kiroStageSHA256(profileBytes)
	compiled := make([]hostGetterGeneratedFamily, 0, len(families))
	for i, family := range families {
		generated, err := generateHostGetterFamily(repoRoot, outputDir, family.name, inputs[i].original, profiles, previousProfiles, profileHash, family.wantHooks, family.wantEdits)
		if err != nil {
			t.Fatalf("generate %s: %v", family.name, err)
		}
		if family.name == "legacy-workflow" {
			if !reflect.DeepEqual(generated.manifest.Patches, legacyPatches) {
				t.Fatal("legacy manifest patches diverge from tools/kiro_stage_bridge.mjs PATCHES")
			}
		}
		if err := runNodeCheck(nodePath, repoRoot, generated.dynamicCandidatePath); err != nil {
			t.Fatal(err)
		}
		if err := runNodeCheck(nodePath, repoRoot, generated.staticCandidatePath); err != nil {
			t.Fatal(err)
		}
		compiled = append(compiled, generated)
	}

	// Back up all tracked destinations into the new temp directory before the
	// first overwrite. The profile has -text in .gitattributes, so bytes remain
	// stable on Windows and Linux.
	paths := []string{
		profilesPath,
		filepath.Join(repoRoot, "proxy", "kiro_stage_bridge_manifest.json"),
		filepath.Join(repoRoot, "proxy", "kiro_stage_bridge_130_manifest.json"),
	}
	for _, path := range paths {
		if err := backupTrackedGeneratorTarget(repoRoot, outputDir, path); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(profilesPath, profileBytes, 0600); err != nil {
		t.Fatal(err)
	}
	for _, generated := range compiled {
		if err := os.WriteFile(generated.manifestPath, generated.manifestBytes, 0600); err != nil {
			t.Fatal(err)
		}
	}

	t.Logf("generated profiles: hooks legacy=35/native=34, adapter_sha256=%s", profileHash)
	for _, generated := range compiled {
		t.Logf("%s original=%s static_candidate=%s->%s profile_candidate=%s output=%s", generated.family,
			generated.originalHash, generated.previousCandidateHash, generated.manifest.CandidateSHA256,
			generated.dynamicCandidateHash, outputDir)
	}
}

type hostGetterGeneratedFamily struct {
	family, originalHash, previousCandidateHash string
	manifest                                    kiroStageManifest
	manifestPath, staticCandidatePath           string
	dynamicCandidatePath                        string
	manifestBytes                               []byte
	dynamicCandidateHash                        string
}

type hostGetterFamilyInput struct {
	family   string
	original []byte
}

func requireNewTemporaryDirectory(path string) error {
	tempRoot, err := filepath.Abs(os.TempDir())
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(tempRoot, path)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("output directory must be a new child of %s", tempRoot)
	}
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("refusing to reuse existing output directory %s", path)
	} else if !os.IsNotExist(err) {
		return err
	}
	return nil
}

func loadHostGetterProfiles(repoRoot string) ([]kiroStageStructureProfile, []kiroStageStructureProfile, string, error) {
	profilesPath := filepath.Join(repoRoot, "proxy", "kiro_stage_structural_profiles.json")
	previousPath := filepath.Join(repoRoot, "proxy", "kiro_stage_structural_profiles_previous.json")
	previousBytes, err := os.ReadFile(previousPath)
	if err != nil {
		return nil, nil, profilesPath, err
	}
	if got := kiroStageSHA256(previousBytes); got != kiroStageHostGetterOldAdapter || got != kiroStagePreviousStructuralAdapterHash() {
		return nil, nil, profilesPath, fmt.Errorf("previous adapter SHA256 mismatch: %s", got)
	}
	var previous []kiroStageStructureProfile
	if err := json.Unmarshal(previousBytes, &previous); err != nil {
		return nil, nil, profilesPath, err
	}
	loadedPrevious, err := loadKiroStagePreviousStructuralProfiles()
	if err != nil || !reflect.DeepEqual(previous, loadedPrevious) {
		return nil, nil, profilesPath, fmt.Errorf("previous profiles differ from the embedded immutable copy: %v", err)
	}
	clonedBytes, err := json.Marshal(previous)
	if err != nil {
		return nil, nil, profilesPath, err
	}
	var profiles []kiroStageStructureProfile
	if err := json.Unmarshal(clonedBytes, &profiles); err != nil {
		return nil, nil, profilesPath, err
	}
	if len(profiles) != 2 || profiles[0].Family != "legacy-workflow" || len(profiles[0].Hooks) != 34 ||
		profiles[1].Family != "native-model-routing" || len(profiles[1].Hooks) != 33 {
		return nil, nil, profilesPath, fmt.Errorf("previous profile family/hook identity changed")
	}
	for i := range profiles {
		if err := updateWorkflowSourceHook(&profiles[i]); err != nil {
			return nil, nil, profilesPath, err
		}
	}
	return profiles, previous, profilesPath, nil
}

func updateWorkflowSourceHook(profile *kiroStageStructureProfile) error {
	var hook *kiroStageStructureHook
	for i := range profile.Hooks {
		if profile.Hooks[i].Name == "workflow-source" {
			if hook != nil {
				return fmt.Errorf("%s has duplicate workflow-source hooks", profile.Family)
			}
			hook = &profile.Hooks[i]
		}
	}
	if hook == nil {
		return fmt.Errorf("%s is missing workflow-source hook", profile.Family)
	}
	for _, replacement := range []struct{ before, after string }{
		{".getSession(", ".getSessionKiroProxyRoute("},
		{")?.kiroProxyRoute", ")"},
	} {
		updated, start, end, err := replaceUniqueASCII(hook.After, replacement.before, replacement.after)
		if err != nil {
			return fmt.Errorf("%s workflow-source %w", profile.Family, err)
		}
		if err := shiftHookOffsets(hook, start, end, len(replacement.after)-(end-start)); err != nil {
			return fmt.Errorf("%s workflow-source: %w", profile.Family, err)
		}
		hook.After = updated
	}
	if strings.Count(hook.After, `parentRoute:t.host.getSessionKiroProxyRoute(p)??{}`) != 1 {
		return fmt.Errorf("%s workflow-source did not become the exact façade call", profile.Family)
	}
	return nil
}

func loadHostGetterOriginal(repoRoot, outputDir, family, sampleName, expectedOriginal string) (hostGetterFamilyInput, error) {
	samplePath := filepath.Join(repoRoot, "build", "kiro-stage-performance-20261014-verified", sampleName)
	sample, err := os.ReadFile(samplePath)
	if err != nil {
		return hostGetterFamilyInput{}, err
	}
	original, record, found, err := parseKiroStageStructuralCandidate(sample)
	if err != nil || !found {
		return hostGetterFamilyInput{}, fmt.Errorf("sample must contain a valid structural restore record: %v", err)
	}
	originalHash := kiroStageSHA256(original)
	if record.AdapterSHA256 != kiroStageHostGetterOldAdapter || originalHash != expectedOriginal || record.OriginalSHA256 != expectedOriginal {
		return hostGetterFamilyInput{}, fmt.Errorf("sample identity mismatch: adapter=%s original=%s record=%s", record.AdapterSHA256, originalHash, record.OriginalSHA256)
	}
	if err := validateKiroStageJS(original); err != nil {
		return hostGetterFamilyInput{}, fmt.Errorf("restored original JavaScript: %w", err)
	}
	if err := writeNewFile(outputDir, family+"-original.js", original); err != nil {
		return hostGetterFamilyInput{}, err
	}
	return hostGetterFamilyInput{family: family, original: original}, nil
}

func replaceUniqueASCII(source, before, after string) (string, int, int, error) {
	if strings.Count(source, before) != 1 {
		return source, 0, 0, fmt.Errorf("expected one %q occurrence", before)
	}
	start := strings.Index(source, before)
	end := start + len(before)
	return source[:start] + after + source[end:], start, end, nil
}

func shiftHookOffsets(hook *kiroStageStructureHook, start, end, delta int) error {
	shift := func(from, to int) (int, int, error) {
		if from < 0 || to <= from || to > len(hook.After) {
			return 0, 0, fmt.Errorf("offset [%d,%d) outside After", from, to)
		}
		if to <= start {
			return from, to, nil
		}
		if from >= end {
			return from + delta, to + delta, nil
		}
		return 0, 0, fmt.Errorf("offset [%d,%d) overlaps replacement [%d,%d)", from, to, start, end)
	}
	for i := range hook.Captures {
		from, to, err := shift(hook.Captures[i].Start, hook.Captures[i].End)
		if err != nil {
			return fmt.Errorf("capture %d: %w", i, err)
		}
		hook.Captures[i].Start, hook.Captures[i].End = from, to
	}
	for i := range hook.Globals {
		from, to, err := shift(hook.Globals[i].Start, hook.Globals[i].End)
		if err != nil {
			return fmt.Errorf("global %d: %w", i, err)
		}
		hook.Globals[i].Start, hook.Globals[i].End = from, to
	}
	return nil
}

func generateHostGetterFamily(repoRoot, outputDir, family string, original []byte,
	profiles, previousProfiles []kiroStageStructureProfile, adapterHash string, wantHooks, wantEdits int) (hostGetterGeneratedFamily, error) {
	originalHash := kiroStageSHA256(original)
	profile := findHostGetterProfile(profiles, family)
	previousProfile := findHostGetterProfile(previousProfiles, family)
	if profile == nil || previousProfile == nil || len(profile.Hooks) != wantHooks {
		return hostGetterGeneratedFamily{}, fmt.Errorf("%s generated hook count mismatch", family)
	}
	if err := verifyPreservedProfileHooks(*previousProfile, *profile); err != nil {
		return hostGetterGeneratedFamily{}, err
	}
	dynamicCandidate, err := buildKiroStageStructuralCandidateWithProfiles(original, profiles, adapterHash)
	if err != nil {
		return hostGetterGeneratedFamily{}, err
	}
	if err := validateKiroStageJS(dynamicCandidate); err != nil {
		return hostGetterGeneratedFamily{}, fmt.Errorf("generated structural candidate JavaScript: %w", err)
	}
	_, generatedRecord, found, err := parseKiroStageStructuralCandidate(dynamicCandidate)
	if err != nil || !found || generatedRecord.AdapterSHA256 != adapterHash || generatedRecord.OriginalSHA256 != originalHash || len(generatedRecord.Edits) != wantEdits {
		return hostGetterGeneratedFamily{}, fmt.Errorf("generated candidate record mismatch: found=%t adapter=%s original=%s edits=%d err=%v", found, generatedRecord.AdapterSHA256, generatedRecord.OriginalSHA256, len(generatedRecord.Edits), err)
	}
	dynamicPath := filepath.Join(outputDir, family+"-profile-candidate.js")
	if err := writeNewFile(outputDir, filepath.Base(dynamicPath), dynamicCandidate); err != nil {
		return hostGetterGeneratedFamily{}, err
	}

	manifestName := "kiro_stage_bridge_manifest.json"
	if family == "native-model-routing" {
		manifestName = "kiro_stage_bridge_130_manifest.json"
	}
	manifestPath := filepath.Join(repoRoot, "proxy", manifestName)
	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		return hostGetterGeneratedFamily{}, err
	}
	var manifest kiroStageManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return hostGetterGeneratedFamily{}, err
	}
	if manifest.OriginalSHA256 != originalHash || manifest.CandidateSHA256 == "" {
		return hostGetterGeneratedFamily{}, fmt.Errorf("%s manifest original/current identity mismatch", family)
	}
	previousCandidateHash := manifest.CandidateSHA256
	previousPatches := append([]kiroStagePatch(nil), manifest.Patches...)
	if err := appendPreviousManifestVersion(&manifest, previousCandidateHash, previousPatches); err != nil {
		return hostGetterGeneratedFamily{}, err
	}
	if err := updateManifestWorkflowSource(&manifest); err != nil {
		return hostGetterGeneratedFamily{}, err
	}
	if err := appendManifestHostGetterPatch(&manifest); err != nil {
		return hostGetterGeneratedFamily{}, err
	}
	manifest.CandidateSHA256 = ""
	staticPayload, err := applyKiroStagePatches(original, manifest.Patches)
	if err != nil {
		return hostGetterGeneratedFamily{}, err
	}
	if err := validateKiroStageJS(staticPayload); err != nil {
		return hostGetterGeneratedFamily{}, fmt.Errorf("generated static candidate JavaScript: %w", err)
	}
	manifest.CandidateSHA256 = kiroStageSHA256(staticPayload)
	staticCandidate, err := buildKiroStageCandidate(original, manifest)
	if err != nil || !bytes.Equal(staticCandidate, staticPayload) {
		return hostGetterGeneratedFamily{}, fmt.Errorf("static manifest candidate recheck: %v", err)
	}
	staticPath := filepath.Join(outputDir, family+"-manifest-candidate.js")
	if err := writeNewFile(outputDir, filepath.Base(staticPath), staticCandidate); err != nil {
		return hostGetterGeneratedFamily{}, err
	}
	manifestBytes, err = json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return hostGetterGeneratedFamily{}, err
	}
	manifestBytes = append(manifestBytes, '\n')

	return hostGetterGeneratedFamily{
		family: family, originalHash: originalHash, previousCandidateHash: previousCandidateHash,
		manifest: manifest, manifestPath: manifestPath, manifestBytes: manifestBytes,
		staticCandidatePath: staticPath, dynamicCandidatePath: dynamicPath,
		dynamicCandidateHash: kiroStageSHA256(dynamicCandidate),
	}, nil
}

func findHostGetterProfile(profiles []kiroStageStructureProfile, family string) *kiroStageStructureProfile {
	for i := range profiles {
		if profiles[i].Family == family {
			return &profiles[i]
		}
	}
	return nil
}

func verifyPreservedProfileHooks(previous, current kiroStageStructureProfile) error {
	if previous.Family != current.Family || len(current.Hooks) < len(previous.Hooks) {
		return fmt.Errorf("%s profile identity changed", previous.Family)
	}
	for i := range previous.Hooks {
		before, after := previous.Hooks[i], current.Hooks[i]
		if before.Name != after.Name {
			return fmt.Errorf("%s hook order changed at %d", previous.Family, i)
		}
		if before.Name != "workflow-source" {
			if !reflect.DeepEqual(before, after) {
				return fmt.Errorf("%s existing hook %q changed", previous.Family, before.Name)
			}
			continue
		}
		if !reflect.DeepEqual(before.Context, after.Context) || before.First != after.First || before.Last != after.Last || before.ValidateOnly != after.ValidateOnly {
			return fmt.Errorf("%s workflow-source context/edit boundary changed", previous.Family)
		}
	}
	return nil
}

func appendHostGetterHook(profile *kiroStageStructureProfile, original []byte, analysis *kiroStageJSAnalysis) error {
	if profile == nil || analysis == nil {
		return fmt.Errorf("missing profile or JavaScript analysis")
	}
	for _, hook := range profile.Hooks {
		if hook.Name == "workflow-host-route-accessor" {
			return fmt.Errorf("%s already contains workflow-host-route-accessor", profile.Family)
		}
	}
	if bytes.Count(original, []byte(kiroStageHostGetterBefore)) != 1 {
		return fmt.Errorf("%s original accessor anchor must be unique", profile.Family)
	}
	start := bytes.Index(original, []byte(kiroStageHostGetterBefore))
	end := start + len(kiroStageHostGetterBefore)
	first, last := -1, -1
	for i, token := range analysis.Tokens {
		if token.Start == start {
			first = i
		}
		if token.End == end {
			last = i + 1
		}
	}
	if first < 0 || last <= first || analysis.Tokens[first].Start != start || analysis.Tokens[last-1].End != end {
		return fmt.Errorf("%s complete accessor anchor does not align to analyzer tokens", profile.Family)
	}
	context := make([]kiroStagePatternToken, 0, last-first)
	var captures []kiroStageCapture
	cBinding := ""
	for _, token := range analysis.Tokens[first:last] {
		pattern := kiroStagePatternToken{Key: token.Key, Shape: token.Shape}
		if token.Key == "V" {
			if token.Binding == "" {
				return fmt.Errorf("%s accessor V token has no binding", profile.Family)
			}
			pattern.Binding = token.Binding
			captures = append(captures, kiroStageCapture{Start: token.Start - start, End: token.End - start, Binding: token.Binding, Declaration: token.Declaration == token.Start})
			if token.Name == "c" {
				if cBinding != "" && cBinding != token.Binding {
					return fmt.Errorf("%s accessor c binding is ambiguous", profile.Family)
				}
				cBinding = token.Binding
			}
		}
		context = append(context, pattern)
	}
	if cBinding == "" {
		return fmt.Errorf("%s accessor helper c was not found", profile.Family)
	}
	after := kiroStageHostGetterBefore + "," + kiroStageHostGetterProperty
	getterC := "," + kiroStageHostGetterProperty
	getterCIndex := strings.Index(getterC, "c(")
	if getterCIndex < 0 || strings.Count(getterC, "c(") != 1 {
		return fmt.Errorf("new accessor helper c is not unique")
	}
	newCaptureStart := len(kiroStageHostGetterBefore) + getterCIndex
	captures = append(captures, kiroStageCapture{Start: newCaptureStart, End: newCaptureStart + 1, Binding: cBinding})
	profile.Hooks = append(profile.Hooks, kiroStageStructureHook{
		Name: "workflow-host-route-accessor", Context: context, First: 0, Last: len(context), After: after, Captures: captures,
	})
	return nil
}

func appendPreviousManifestVersion(manifest *kiroStageManifest, candidateHash string, patches []kiroStagePatch) error {
	for _, previous := range manifest.PreviousVersions {
		if previous.CandidateSHA256 == candidateHash {
			if !reflect.DeepEqual(previous.Patches, patches) {
				return fmt.Errorf("previous manifest hash %s has different patches", candidateHash)
			}
			return nil
		}
	}
	manifest.PreviousVersions = append(manifest.PreviousVersions, kiroStagePreviousVersion{CandidateSHA256: candidateHash, Patches: patches})
	return nil
}

func updateManifestWorkflowSource(manifest *kiroStageManifest) error {
	count := 0
	for i := range manifest.Patches {
		if manifest.Patches[i].Name != "workflow-source" {
			continue
		}
		updated, _, _, err := replaceUniqueASCII(manifest.Patches[i].After, kiroStageHostGetterOldCall, kiroStageHostGetterNewCall)
		if err != nil {
			return fmt.Errorf("manifest workflow-source: %w", err)
		}
		manifest.Patches[i].After = updated
		count++
	}
	if count != 1 {
		return fmt.Errorf("manifest must contain exactly one workflow-source patch, got %d", count)
	}
	return nil
}

func appendManifestHostGetterPatch(manifest *kiroStageManifest) error {
	for _, patch := range manifest.Patches {
		if patch.Name == kiroStageHostGetterPatchName {
			return fmt.Errorf("manifest already contains %s", kiroStageHostGetterPatchName)
		}
	}
	manifest.Patches = append(manifest.Patches, kiroStagePatch{
		Name:   kiroStageHostGetterPatchName,
		Before: kiroStageHostGetterBefore,
		After:  kiroStageHostGetterBefore + "," + kiroStageHostGetterProperty,
	})
	return nil
}

func applyKiroStagePatches(original []byte, patches []kiroStagePatch) ([]byte, error) {
	candidate := append([]byte(nil), original...)
	for _, patch := range patches {
		before := []byte(patch.Before)
		if count := bytes.Count(candidate, before); count != 1 {
			return nil, fmt.Errorf("patch %q anchor count = %d", patch.Name, count)
		}
		candidate = bytes.Replace(candidate, before, []byte(patch.After), 1)
	}
	return candidate, nil
}

func readLegacyBridgePatches(nodePath, repoRoot string) ([]kiroStagePatch, error) {
	source := `import {PATCHES} from './tools/kiro_stage_bridge.mjs';process.stdout.write(JSON.stringify(PATCHES.map(([name,before,after])=>({name,before,after}))))`
	command := exec.Command(nodePath, "--input-type=module", "-e", source)
	command.Dir = repoRoot
	var output bytes.Buffer
	command.Stdout = &output
	var errorOutput bytes.Buffer
	command.Stderr = &errorOutput
	if err := command.Run(); err != nil {
		return nil, fmt.Errorf("read legacy source PATCHES: %w: %s", err, errorOutput.String())
	}
	var patches []kiroStagePatch
	if err := json.Unmarshal(output.Bytes(), &patches); err != nil {
		return nil, err
	}
	return patches, nil
}

func runNodeCheck(nodePath, repoRoot, path string) error {
	command := exec.Command(nodePath, "--check", path)
	command.Dir = repoRoot
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("node --check %s: %w: %s", path, err, output)
	}
	return nil
}

func writeNewFile(directory, name string, data []byte) error {
	path := filepath.Join(directory, name)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func backupTrackedGeneratorTarget(repoRoot, outputDir, path string) error {
	relative, err := filepath.Rel(repoRoot, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("generator target escaped repository: %s", path)
	}
	command := exec.Command("git", "ls-files", "--error-unmatch", filepath.ToSlash(relative))
	command.Dir = repoRoot
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("generator target is not tracked: %s: %s", relative, output)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	name := strings.ReplaceAll(filepath.ToSlash(relative), "/", "_") + ".before"
	return writeNewFile(outputDir, name, data)
}
