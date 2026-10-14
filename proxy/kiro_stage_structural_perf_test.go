package proxy

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestKiroStageIndexedContextMatchesKMPOracle(t *testing.T) {
	pattern := []kiroStagePatternToken{
		{Key: "V", Binding: "left", Shape: "Variable;"},
		{Key: "P:=", Shape: "Assign;"},
		{Key: "V", Binding: "right", Shape: "Variable;"},
		{Key: "P:+", Shape: "Add;"},
		{Key: "V", Binding: "left", Shape: "Variable;"},
	}
	tokens := []kiroStageJSToken{{Key: "P:;", Shape: "Noise;", Declaration: -1}}
	appendCandidate := func(bindings []string, shapes []string) {
		for i, expected := range pattern {
			binding := ""
			if expected.Binding != "" {
				binding = bindings[i]
			}
			shape := expected.Shape
			if shapes != nil {
				shape = shapes[i]
			}
			tokens = append(tokens, kiroStageJSToken{Key: expected.Key, Shape: shape, Binding: binding, Declaration: -1})
		}
		tokens = append(tokens, kiroStageJSToken{Key: "P:;", Shape: "Noise;", Declaration: -1})
	}
	appendCandidate([]string{"b:1:x", "", "b:1:y", "", "b:1:x"}, nil)
	appendCandidate([]string{"b:2:x", "", "b:2:y", "", "b:2:z"}, nil) // 同一 pattern binding 分裂。
	appendCandidate([]string{"b:3:x", "", "b:3:x", "", "b:3:x"}, nil) // 两个 pattern binding 被合并。
	appendCandidate([]string{"b:4:x", "", "b:4:y", "", "b:4:x"}, []string{"Wrong;", "Assign;", "Variable;", "Add;", "Variable;"})
	appendCandidate([]string{"b:5:x", "", "b:5:y", "", "b:5:x"}, nil)

	index := buildKiroStageContextIndex(tokens)
	want := findKiroStageContext(tokens, pattern)
	got := findKiroStageContextIndexed(tokens, index, pattern)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("indexed matches %v differ from KMP oracle %v", got, want)
	}
	if len(got) != 2 {
		t.Fatalf("valid duplicate contexts = %v, want two matches to preserve ambiguity", got)
	}
}

func TestKiroStageStructuralProfileSelectionOrder(t *testing.T) {
	analysis := &kiroStageJSAnalysis{Tokens: []kiroStageJSToken{{
		Start: 10, End: 11, Key: "V", Name: "renamed", Binding: "b:1:renamed", Declaration: 10, Shape: "Declaration;",
	}}}
	index := buildKiroStageContextIndex(analysis.Tokens)
	miss := kiroStageStructureProfile{Family: "miss", Hooks: []kiroStageStructureHook{{
		Name: "missing", Context: []kiroStagePatternToken{{Key: "P:missing", Shape: "Missing;"}}, First: 0, Last: 1, After: "1",
	}}}
	match := kiroStageStructureProfile{Family: "match", Hooks: []kiroStageStructureHook{{
		Name: "target", Context: []kiroStagePatternToken{{Key: "V", Binding: "value", Shape: "Declaration;"}}, First: 0, Last: 1, After: "1",
	}}}
	for _, profiles := range [][]kiroStageStructureProfile{{miss, match}, {match, miss}} {
		selected, edits, err := selectKiroStageStructuralProfile(analysis, index, profiles)
		if err != nil || selected == nil || selected.Family != "match" || len(edits) != 1 || edits[0].start != 10 || edits[0].end != 11 {
			t.Fatalf("selection = %#v, edits %#v, error %v", selected, edits, err)
		}
	}
	ambiguous := kiroStageStructureProfile{Family: "other-match", Hooks: []kiroStageStructureHook{{
		Name: "same-target", Context: []kiroStagePatternToken{{Key: "V", Binding: "same", Shape: "Declaration;"}}, First: 0, Last: 1, After: "2",
	}}}
	for _, profiles := range [][]kiroStageStructureProfile{{match, ambiguous}, {ambiguous, match}} {
		_, _, err := selectKiroStageStructuralProfile(analysis, index, profiles)
		if err == nil || !strings.Contains(err.Error(), "结构族匹配存在歧义") {
			t.Fatalf("ambiguous profiles error = %v", err)
		}
	}
}

func TestKiroStageStructuralPlanMapsGlobalsAfterCapture(t *testing.T) {
	analysis := &kiroStageJSAnalysis{Tokens: []kiroStageJSToken{{
		Start: 4, End: 5, Key: "V", Name: "longSourceName", Binding: "b:1:longSourceName", Declaration: 4, Shape: "Variable;",
	}}}
	after := "x + Array"
	globalStart := strings.Index(after, "Array")
	profile := kiroStageStructureProfile{Family: "mock", Hooks: []kiroStageStructureHook{{
		Name:    "capture-before-global",
		Context: []kiroStagePatternToken{{Key: "V", Binding: "value", Shape: "Variable;"}},
		First:   0, Last: 1, After: after,
		Captures: []kiroStageCapture{{Start: 0, End: 1, Binding: "value"}},
		Globals:  []kiroStageStructureGlobal{{Start: globalStart, End: globalStart + len("Array"), Name: "Array"}},
	}}}
	edits, err := planKiroStageStructure(analysis, profile)
	if err != nil || len(edits) != 1 {
		t.Fatalf("plan edits = %#v, error %v", edits, err)
	}
	wantAfter := "longSourceName + Array"
	wantGlobalStart := strings.Index(wantAfter, "Array")
	if edits[0].after != wantAfter || len(edits[0].globals) != 1 || edits[0].globals[0].start != wantGlobalStart || edits[0].globals[0].end != wantGlobalStart+len("Array") {
		t.Fatalf("planned edit = %#v, want after %q and exact global offsets", edits[0], wantAfter)
	}
}

func TestKiroStageInjectedBindingsRequireExactEnvironmentSources(t *testing.T) {
	const original = "const existing=0;\n"
	globalAfter := "const kiroProxyGlobalV3_Array=Array;\n"
	globalStart := strings.LastIndex(globalAfter, "Array")
	validGlobal := kiroStageSourceEdit{start: len(original), end: len(original), after: globalAfter, globals: []kiroStageResolvedGlobal{{start: globalStart, end: globalStart + len("Array"), name: "Array"}}}
	if err := verifyInjectedFixture(t, original, globalAfter, validGlobal); err != nil {
		t.Fatalf("registered free global rejected: %v", err)
	}
	if err := verifyInjectedFixture(t, original, globalAfter, kiroStageSourceEdit{start: len(original), end: len(original), after: globalAfter}); err == nil || !strings.Contains(err.Error(), "globals") && !strings.Contains(err.Error(), "自由引用") {
		t.Fatalf("unregistered free global error = %v", err)
	}
	wrongGlobal := validGlobal
	wrongGlobal.globals = []kiroStageResolvedGlobal{{start: globalStart, end: globalStart + len("Array"), name: "Buffer"}}
	if err := verifyInjectedFixture(t, original, globalAfter, wrongGlobal); err == nil {
		t.Fatal("free global with a mismatched registered name was accepted")
	}

	const capturedOriginal = "const input=1;\n"
	const capturedAfter = "const kiroProxyLocalV3_copy=input;\n"
	inputStart := strings.LastIndex(capturedAfter, "input")
	if err := verifyInjectedFixture(t, capturedOriginal, capturedAfter, kiroStageSourceEdit{start: len(capturedOriginal), end: len(capturedOriginal), after: capturedAfter}); err == nil || !strings.Contains(err.Error(), "capture") {
		t.Fatalf("uncaptured original binding error = %v", err)
	}
	originalAnalysis, err := analyzeKiroStageJS([]byte(capturedOriginal))
	if err != nil {
		t.Fatal(err)
	}
	inputDeclaration := -1
	for _, token := range originalAnalysis.Tokens {
		if token.Name == "input" && token.Key == "V" {
			inputDeclaration = token.Declaration
			break
		}
	}
	if inputDeclaration < 0 {
		t.Fatal("mock input declaration was not analyzed")
	}
	capturedEdit := kiroStageSourceEdit{
		start: len(capturedOriginal), end: len(capturedOriginal), after: capturedAfter,
		captures: []kiroStageResolvedCapture{{start: inputStart, end: inputStart + len("input"), originalDeclaration: inputDeclaration}},
	}
	if err := verifyInjectedFixture(t, capturedOriginal, capturedAfter, capturedEdit); err != nil {
		t.Fatalf("exact original capture rejected: %v", err)
	}

	const unreservedAfter = "const alias=1;\n"
	if err := verifyInjectedFixture(t, original, unreservedAfter, kiroStageSourceEdit{start: len(original), end: len(original), after: unreservedAfter}); err == nil || !strings.Contains(err.Error(), "保留前缀") {
		t.Fatalf("unreserved injected declaration error = %v", err)
	}
	if err := verifyInjectedFixture(t, "const Array=1;\n", globalAfter, kiroStageSourceEdit{
		start: len("const Array=1;\n"), end: len("const Array=1;\n"), after: globalAfter,
		globals: []kiroStageResolvedGlobal{{start: globalStart, end: globalStart + len("Array"), name: "Array"}},
	}); err == nil {
		t.Fatal("profile global captured by original binding was accepted")
	}
}

func verifyInjectedFixture(t *testing.T, original, after string, edit kiroStageSourceEdit) error {
	t.Helper()
	candidate := append(append([]byte(nil), original...), after...)
	analysis, err := analyzeKiroStageJS(candidate)
	if err != nil {
		t.Fatalf("analyze injected fixture: %v", err)
	}
	return verifyInjectedAnalysisFixture(t, analysis, []kiroStageSourceEdit{edit})
}

func verifyInjectedAnalysisFixture(t *testing.T, analysis *kiroStageJSAnalysis, edits []kiroStageSourceEdit) error {
	t.Helper()
	fastErr := verifyKiroStageInjectedBindings(analysis, edits)
	referenceErr := verifyKiroStageInjectedBindingsReference(analysis, edits)
	if (fastErr == nil) != (referenceErr == nil) {
		t.Fatalf("ordered verifier result %v differs from full-scan oracle %v", fastErr, referenceErr)
	}
	return fastErr
}

func TestKiroStageInjectedBindingsIgnoreFixedPropertyNames(t *testing.T) {
	const after = "({ Array: 1, Buffer: 2 }); Array"
	analysis, err := analyzeKiroStageJS([]byte(after))
	if err != nil {
		t.Fatal(err)
	}
	propertyStart, globalStart := strings.Index(after, "Array"), strings.LastIndex(after, "Array")
	var property, global kiroStageJSToken
	for _, token := range analysis.Tokens {
		switch token.Start {
		case propertyStart:
			property = token
		case globalStart:
			global = token
		}
	}
	if property.Name != "Array" || property.Reference || global.Name != "Array" || !global.Reference || property.Key != global.Key || property.Binding != global.Binding || property.Declaration != global.Declaration {
		t.Fatalf("fixture must distinguish otherwise identical property and free-reference tokens: property=%+v global=%+v", property, global)
	}
	edit := kiroStageSourceEdit{start: 0, end: 0, after: after, globals: []kiroStageResolvedGlobal{{start: globalStart, end: globalStart + len("Array"), name: "Array"}}}
	if err := verifyInjectedAnalysisFixture(t, analysis, []kiroStageSourceEdit{edit}); err != nil {
		t.Fatalf("fixed property names were treated as free references: %v", err)
	}
}

func TestKiroStageInjectedBindingsRejectShadowedRegisteredGlobals(t *testing.T) {
	names := []string{"Array", "Buffer", "Error", "JSON", "Map", "Object", "Set", "String", "encodeURIComponent", "process", "require", "undefined"}
	shadowSources := []struct {
		name   string
		source func(string) string
	}{
		{name: "parameter", source: func(name string) string { return "function test(" + name + "){return 0;}" }},
		{name: "var", source: func(name string) string { return "function test(){var " + name + ";return 0;}" }},
		{name: "let", source: func(name string) string { return "function test(){let " + name + ";return 0;}" }},
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			t.Run("unshadowed", func(t *testing.T) {
				if err := verifyKiroStageStructuralGlobalFixture(t, "function test(){return 0;}", name); err != nil {
					t.Fatalf("unshadowed registered global rejected: %v", err)
				}
			})
			for _, shadow := range shadowSources {
				t.Run(shadow.name, func(t *testing.T) {
					if err := verifyKiroStageStructuralGlobalFixture(t, shadow.source(name), name); err == nil {
						t.Fatalf("registered global was captured by same-name %s binding", shadow.name)
					}
				})
			}
		})
	}
}

func TestKiroStageInjectedBindingsRejectMathParameterCapture(t *testing.T) {
	if err := verifyKiroStageStructuralGlobalFixture(t, "function test(Math){return 0;}", "Math"); err == nil {
		t.Fatal("registered Math global resolved to the same-name parameter")
	}
}

func verifyKiroStageStructuralGlobalFixture(t *testing.T, originalSource, name string) error {
	t.Helper()
	original := []byte(originalSource)
	originalBefore := append([]byte(nil), original...)
	insertion := strings.Index(originalSource, "return")
	if insertion < 0 {
		t.Fatalf("fixture has no insertion point: %q", originalSource)
	}
	after := "const kiroProxyGlobalV3_" + name + "=" + name + ";"
	candidate := make([]byte, 0, len(original)+len(after))
	candidate = append(candidate, original[:insertion]...)
	candidate = append(candidate, after...)
	candidate = append(candidate, original[insertion:]...)
	candidateBefore := append([]byte(nil), candidate...)
	analysis, err := analyzeKiroStageJS(candidate)
	if err != nil {
		t.Fatalf("analyze candidate fixture: %v", err)
	}
	if !reflect.DeepEqual(original, originalBefore) || !reflect.DeepEqual(candidate, candidateBefore) {
		t.Fatal("analysis modified original or candidate source")
	}
	globalStart := strings.LastIndex(after, "=") + 1
	edit := kiroStageSourceEdit{
		start: insertion, end: insertion, after: after,
		globals: []kiroStageResolvedGlobal{{start: globalStart, end: globalStart + len(name), name: name}},
	}
	err = verifyInjectedAnalysisFixture(t, analysis, []kiroStageSourceEdit{edit})
	if !reflect.DeepEqual(original, originalBefore) || !reflect.DeepEqual(candidate, candidateBefore) {
		t.Fatal("verification modified original or candidate source")
	}
	return err
}

func TestKiroStageStructuralProfileRejectsASISourceMock(t *testing.T) {
	const ordinary = "function f(){return value}"
	const withASI = "function f(){return\nvalue}"
	ordinaryAnalysis, err := analyzeKiroStageJS([]byte(ordinary))
	if err != nil {
		t.Fatal(err)
	}
	var valueToken kiroStageJSToken
	for _, token := range ordinaryAnalysis.Tokens {
		if token.Start == strings.LastIndex(ordinary, "value") {
			valueToken = token
		}
	}
	if valueToken.Key == "" {
		t.Fatal("ASI source mock tokens were not analyzed")
	}
	asiAnalysis, err := analyzeKiroStageJS([]byte(withASI))
	if err != nil {
		t.Fatal(err)
	}
	var asiValueToken kiroStageJSToken
	for _, token := range asiAnalysis.Tokens {
		if token.Start == strings.LastIndex(withASI, "value") {
			asiValueToken = token
		}
	}
	if asiValueToken.Key == "" || asiValueToken.Shape == valueToken.Shape {
		t.Fatalf("ASI mock did not alter returned-value AST shape: ordinary=%+v ASI=%+v", valueToken, asiValueToken)
	}
	profile := kiroStageStructureProfile{Family: "asi-mock", Hooks: []kiroStageStructureHook{{
		Name: "returned-value", ValidateOnly: true,
		Context: []kiroStagePatternToken{{Key: valueToken.Key, Shape: valueToken.Shape}},
	}}}
	if _, err := planKiroStageStructure(ordinaryAnalysis, profile); err != nil {
		t.Fatalf("ordinary mock source did not match: %v", err)
	}
	if _, err := planKiroStageStructure(asiAnalysis, profile); err == nil {
		t.Fatal("profile accepted the source after return ASI changed its AST shape")
	}
}

// Deliberately scans every token for every edit. It is a small mock oracle for
// the ordered merge implementation, not a production path.
func verifyKiroStageInjectedBindingsReference(installed *kiroStageJSAnalysis, edits []kiroStageSourceEdit) error {
	tokens := installed.Tokens
	bases, ends := make([]int, len(edits)), make([]int, len(edits))
	captures, globals := map[int]kiroStageResolvedCapture{}, map[int]kiroStageResolvedGlobal{}
	capturedDeclarations, reservedBindings := map[int]int{}, map[string]int{}
	delta, previousEnd := 0, 0
	for i, edit := range edits {
		if edit.start < previousEnd || edit.end < edit.start {
			return errors.New("full-scan oracle rejected injected binding")
		}
		base := edit.start + delta
		bases[i], ends[i] = base, base+len(edit.after)
		for _, capture := range edit.captures {
			captures[base+capture.start] = capture
		}
		for _, global := range edit.globals {
			globals[base+global.start] = kiroStageResolvedGlobal{start: base + global.start, end: base + global.end, name: global.name}
		}
		delta += len(edit.after) - (edit.end - edit.start)
		previousEnd = edit.end
	}
	for i := range edits {
		for _, token := range tokens {
			if token.Start < bases[i] || token.Start >= ends[i] || !token.Reference || token.Name == "" || token.Binding == "" || token.Declaration != token.Start {
				continue
			}
			if capture, ok := captures[token.Start]; ok && capture.declaration {
				if token.End != bases[i]+capture.end {
					return errors.New("full-scan oracle rejected injected binding")
				}
				if prior, exists := capturedDeclarations[capture.originalDeclaration]; exists && prior != token.Start {
					return errors.New("full-scan oracle rejected injected binding")
				}
				capturedDeclarations[capture.originalDeclaration] = token.Start
			} else {
				if !strings.HasPrefix(token.Name, "kiroProxy") {
					return errors.New("full-scan oracle rejected injected binding")
				}
				reservedBindings[token.Binding] = token.Declaration
			}
		}
	}
	mapDeclaration := func(offset int) (int, bool) {
		if position, ok := capturedDeclarations[offset]; ok {
			return position, true
		}
		shift := 0
		for _, edit := range edits {
			if offset < edit.start {
				break
			}
			if offset < edit.end {
				return 0, false
			}
			shift += len(edit.after) - (edit.end - edit.start)
		}
		return offset + shift, offset >= 0
	}
	for i, edit := range edits {
		for _, capture := range edit.captures {
			position := bases[i] + capture.start
			var token kiroStageJSToken
			found := false
			for _, candidate := range tokens {
				if candidate.Start == position {
					token, found = candidate, true
					break
				}
			}
			expected, mapped := mapDeclaration(capture.originalDeclaration)
			if !found || !mapped || token.End != bases[i]+capture.end || !token.Reference || token.Binding == "" || token.Declaration != expected || (token.Declaration == token.Start) != capture.declaration {
				return errors.New("full-scan oracle rejected injected binding")
			}
		}
	}
	matchedGlobals := map[int]bool{}
	for i := range edits {
		for _, token := range tokens {
			if token.Start < bases[i] || token.Start >= ends[i] || !token.Reference {
				continue
			}
			if capture, ok := captures[token.Start]; ok {
				if token.End != bases[i]+capture.end {
					return errors.New("full-scan oracle rejected injected binding")
				}
			} else if token.Binding != "" {
				if declaration, ok := reservedBindings[token.Binding]; !ok || token.Declaration != declaration {
					return errors.New("full-scan oracle rejected injected binding")
				}
			} else {
				global, ok := globals[token.Start]
				if !ok || token.End != global.end || token.Name != global.name || token.Key != "I:"+global.name || token.Declaration != -1 {
					return errors.New("full-scan oracle rejected injected binding")
				}
				matchedGlobals[token.Start] = true
			}
		}
	}
	if len(matchedGlobals) != len(globals) {
		return errors.New("full-scan oracle rejected injected binding")
	}
	return nil
}

func BenchmarkKiroStageContextSearch(b *testing.B) {
	tokens, pattern := kiroStageContextBenchmarkFixture(50000, 64)
	index := buildKiroStageContextIndex(tokens)
	b.Run("KMP", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if len(findKiroStageContext(tokens, pattern)) != 1 {
				b.Fatal("KMP benchmark fixture lost its match")
			}
		}
	})
	b.Run("indexed", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if len(findKiroStageContextIndexed(tokens, index, pattern)) != 1 {
				b.Fatal("indexed benchmark fixture lost its match")
			}
		}
	})
}

func kiroStageContextBenchmarkFixture(tokenCount, patternSize int) ([]kiroStageJSToken, []kiroStagePatternToken) {
	tokens := make([]kiroStageJSToken, tokenCount)
	for i := range tokens {
		tokens[i] = kiroStageJSToken{Key: "P:noise", Shape: "Noise;", Declaration: -1}
	}
	start := tokenCount - patternSize
	pattern := make([]kiroStagePatternToken, patternSize)
	for i := range pattern {
		key, shape := "P:common", "Common;"
		if i == patternSize/2 {
			key, shape = "I:anchor", "Anchor;"
		}
		binding := ""
		if i == 2 || i == 4 {
			binding = "value"
		}
		pattern[i] = kiroStagePatternToken{Key: key, Shape: shape, Binding: binding}
		tokenBinding := ""
		if binding != "" {
			tokenBinding = "b:benchmark:value"
		}
		tokens[start+i] = kiroStageJSToken{Key: key, Shape: shape, Binding: tokenBinding, Declaration: -1}
	}
	return tokens, pattern
}
