package proxy

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
)

//go:embed kiro_stage_structural_profiles.json
var kiroStageStructuralProfiles []byte

type kiroStagePatternToken struct {
	Key     string `json:"key"`
	Binding string `json:"binding,omitempty"`
	Shape   string `json:"shape"`
}

type kiroStageCapture struct {
	Start       int    `json:"start"`
	End         int    `json:"end"`
	Binding     string `json:"binding"`
	Declaration bool   `json:"declaration,omitempty"`
}

type kiroStageStructureHook struct {
	Name         string                     `json:"name"`
	Context      []kiroStagePatternToken    `json:"context"`
	First        int                        `json:"first"`
	Last         int                        `json:"last"` // 被替换的 token 区间，左闭右开。
	After        string                     `json:"after"`
	Captures     []kiroStageCapture         `json:"captures,omitempty"`
	Globals      []kiroStageStructureGlobal `json:"globals,omitempty"`
	ValidateOnly bool                       `json:"validate_only,omitempty"`
}

type kiroStageStructureGlobal struct {
	Start int    `json:"start"`
	End   int    `json:"end"`
	Name  string `json:"name"`
}

type kiroStageStructureProfile struct {
	Family string                   `json:"family"`
	Hooks  []kiroStageStructureHook `json:"hooks"`
}

type kiroStageSourceEdit struct {
	start, end int
	after      string
	captures   []kiroStageResolvedCapture
	globals    []kiroStageResolvedGlobal
}

type kiroStageResolvedCapture struct {
	start, end, originalDeclaration int
	declaration                     bool
}

type kiroStageResolvedGlobal struct {
	start, end int
	name       string
}

var kiroStageStructuralProfileCache struct {
	once        sync.Once
	profiles    []kiroStageStructureProfile
	adapterHash string
	err         error
}

func loadKiroStageStructuralProfiles() ([]kiroStageStructureProfile, error) {
	kiroStageStructuralProfileCache.once.Do(func() {
		kiroStageStructuralProfileCache.adapterHash = kiroStageSHA256(kiroStageStructuralProfiles)
		if err := json.Unmarshal(kiroStageStructuralProfiles, &kiroStageStructuralProfileCache.profiles); err != nil {
			kiroStageStructuralProfileCache.err = fmt.Errorf("读取自动桥接结构适配器失败: %w", err)
		}
	})
	return kiroStageStructuralProfileCache.profiles, kiroStageStructuralProfileCache.err
}

// kiroStageStructuralAdapterHash 返回不可变嵌入式结构适配器的缓存摘要。
func kiroStageStructuralAdapterHash() string {
	_, _ = loadKiroStageStructuralProfiles()
	return kiroStageStructuralProfileCache.adapterHash
}

func buildKiroStageStructuralCandidate(original []byte) ([]byte, error) {
	if bytes.Contains(original, []byte(kiroStageStructuralRecordPrefix)) {
		return nil, fmt.Errorf("Kiro 自动桥接输入仍含安装记录")
	}
	analysis, err := analyzeKiroStageJS(original)
	if err != nil {
		return nil, err
	}
	for _, token := range analysis.Tokens {
		if strings.HasPrefix(token.Name, "kiroProxy") {
			return nil, fmt.Errorf("Kiro 源码含未验证或部分安装的桥接标识符 %q", token.Name)
		}
	}
	profiles, err := loadKiroStageStructuralProfiles()
	if err != nil {
		return nil, err
	}
	index := buildKiroStageContextIndex(analysis.Tokens)
	selected, plans, err := selectKiroStageStructuralProfile(analysis, index, profiles)
	if err != nil {
		return nil, err
	}
	// 候选由原版上的非重叠编辑一次生成，避免先改写再找锚点的级联歧义。
	sort.Slice(plans, func(i, j int) bool { return plans[i].start < plans[j].start })
	const placeholder = "__KIRO_ORIGINAL_SHA256__"
	originalHash := kiroStageSHA256(original)
	payloadCapacity := len(original)
	for _, edit := range plans {
		payloadCapacity += len(edit.after) - (edit.end - edit.start)
		payloadCapacity += strings.Count(edit.after, placeholder) * (len(originalHash) - len(placeholder))
	}
	payload := make([]byte, 0, payloadCapacity)
	var recordEdits []kiroStageStructuralEdit
	previous := 0
	for i := range plans {
		edit := &plans[i]
		if edit.start < previous || edit.end > len(original) || edit.end < edit.start {
			return nil, fmt.Errorf("Kiro 自动桥接编辑范围无效或重叠")
		}
		payload = append(payload, original[previous:edit.start]...)
		edit.after, err = replaceKiroStageOriginalHash(edit, placeholder, originalHash)
		if err != nil {
			return nil, err
		}
		after := edit.after
		recordEdits = append(recordEdits, kiroStageStructuralEdit{Offset: len(payload), Original: string(original[edit.start:edit.end]), Installed: after})
		payload = append(payload, after...)
		previous = edit.end
	}
	payload = append(payload, original[previous:]...)
	installed, err := analyzeKiroStageJS(payload)
	if err != nil {
		return nil, fmt.Errorf("Kiro 自动桥接候选作用域检查失败: %w", err)
	}
	if err := verifyKiroStageInjectedBindings(installed, plans); err != nil {
		return nil, err
	}
	return wrapKiroStageStructuralCandidate(original, payload, selected.Family, kiroStageStructuralAdapterHash(), recordEdits)
}

func selectKiroStageStructuralProfile(analysis *kiroStageJSAnalysis, index *kiroStageContextIndex, profiles []kiroStageStructureProfile) (*kiroStageStructureProfile, []kiroStageSourceEdit, error) {
	var selected *kiroStageStructureProfile
	var plans []kiroStageSourceEdit
	var failures []string
	for i := range profiles {
		edits, err := planKiroStageStructureIndexed(analysis, index, profiles[i])
		if err != nil {
			failures = append(failures, profiles[i].Family+": "+err.Error())
			continue
		}
		if selected != nil {
			return nil, nil, fmt.Errorf("Kiro 自动桥接结构族匹配存在歧义")
		}
		selected, plans = &profiles[i], edits
	}
	if selected == nil {
		return nil, nil, fmt.Errorf("Kiro 自动桥接结构不兼容: %s", strings.Join(failures, "; "))
	}
	return selected, plans, nil
}

func replaceKiroStageOriginalHash(edit *kiroStageSourceEdit, placeholder, originalHash string) (string, error) {
	if !strings.Contains(edit.after, placeholder) {
		return edit.after, nil
	}
	for position := strings.LastIndex(edit.after, placeholder); position >= 0; position = strings.LastIndex(edit.after[:position], placeholder) {
		replacedEnd := position + len(placeholder)
		delta := len(originalHash) - len(placeholder)
		for i := range edit.captures {
			if edit.captures[i].start >= replacedEnd {
				edit.captures[i].start += delta
				edit.captures[i].end += delta
			} else if edit.captures[i].end > position {
				return "", fmt.Errorf("Kiro 自动桥接摘要占位符与 capture 重叠")
			}
		}
		for i := range edit.globals {
			if edit.globals[i].start >= replacedEnd {
				edit.globals[i].start += delta
				edit.globals[i].end += delta
			} else if edit.globals[i].end > position {
				return "", fmt.Errorf("Kiro 自动桥接摘要占位符与 globals 重叠")
			}
		}
		edit.after = edit.after[:position] + originalHash + edit.after[replacedEnd:]
	}
	return edit.after, nil
}

func planKiroStageStructure(analysis *kiroStageJSAnalysis, profile kiroStageStructureProfile) ([]kiroStageSourceEdit, error) {
	return planKiroStageStructureIndexed(analysis, buildKiroStageContextIndex(analysis.Tokens), profile)
}

func planKiroStageStructureIndexed(analysis *kiroStageJSAnalysis, index *kiroStageContextIndex, profile kiroStageStructureProfile) ([]kiroStageSourceEdit, error) {
	bindings := map[string]string{}
	names := map[string]string{}
	declarations := map[string]int{}
	reverse := map[string]string{}
	starts := make([]int, len(profile.Hooks))
	for i, hook := range profile.Hooks {
		matches := findKiroStageContextIndexed(analysis.Tokens, index, hook.Context)
		// 角色入口可由前面已唯一识别的调用链绑定进一步约束，仍须全局唯一。
		filtered := matches[:0]
		for _, match := range matches {
			compatible := true
			for j, pattern := range hook.Context {
				if pattern.Binding == "" {
					continue
				}
				binding := analysis.Tokens[match+j].Binding
				if prior, ok := bindings[pattern.Binding]; ok && prior != binding {
					compatible = false
					break
				}
				if prior, ok := reverse[binding]; ok && prior != pattern.Binding {
					compatible = false
					break
				}
			}
			if compatible {
				filtered = append(filtered, match)
			}
		}
		matches = filtered
		if len(matches) != 1 {
			return nil, fmt.Errorf("结构 %q 必须唯一匹配，实际 %d", hook.Name, len(matches))
		}
		start := matches[0]
		starts[i] = start
		for j, pattern := range hook.Context {
			if pattern.Binding == "" {
				continue
			}
			token := analysis.Tokens[start+j]
			if prior, ok := bindings[pattern.Binding]; ok && prior != token.Binding {
				return nil, fmt.Errorf("结构 %q 跨上下文变量绑定不一致", hook.Name)
			}
			if prior, ok := reverse[token.Binding]; ok && prior != pattern.Binding {
				return nil, fmt.Errorf("结构 %q 合并了不同变量绑定", hook.Name)
			}
			bindings[pattern.Binding], names[pattern.Binding], reverse[token.Binding] = token.Binding, token.Name, pattern.Binding
			declarations[pattern.Binding] = token.Declaration
		}
	}
	var edits []kiroStageSourceEdit
	for i, hook := range profile.Hooks {
		if hook.ValidateOnly {
			if len(hook.Globals) != 0 {
				return nil, fmt.Errorf("结构 %q 校验钩子不能登记注入 globals", hook.Name)
			}
			continue
		}
		if hook.First < 0 || hook.Last <= hook.First || hook.Last > len(hook.Context) {
			return nil, fmt.Errorf("结构 %q 内嵌编辑区间无效", hook.Name)
		}
		var output strings.Builder
		var captures []kiroStageResolvedCapture
		previous := 0
		for _, capture := range hook.Captures {
			name, ok := names[capture.Binding]
			if !ok || name == "" || capture.Start < previous || capture.End > len(hook.After) || capture.Start >= capture.End {
				return nil, fmt.Errorf("结构 %q 缺少已验证的注入变量", hook.Name)
			}
			output.WriteString(hook.After[previous:capture.Start])
			captures = append(captures, kiroStageResolvedCapture{output.Len(), output.Len() + len(name), declarations[capture.Binding], capture.Declaration})
			output.WriteString(name)
			previous = capture.End
		}
		output.WriteString(hook.After[previous:])
		globals := make([]kiroStageResolvedGlobal, 0, len(hook.Globals))
		previousGlobal := -1
		for _, global := range hook.Globals {
			if global.Name == "" || global.Start < previousGlobal || global.Start < 0 || global.End <= global.Start || global.End > len(hook.After) || hook.After[global.Start:global.End] != global.Name {
				return nil, fmt.Errorf("结构 %q globals 源区间或名称无效", hook.Name)
			}
			adjustment := 0
			for _, capture := range hook.Captures {
				if capture.End <= global.Start {
					adjustment += len(names[capture.Binding]) - (capture.End - capture.Start)
					continue
				}
				if capture.Start < global.End {
					return nil, fmt.Errorf("结构 %q globals 与 capture 重叠", hook.Name)
				}
				break
			}
			globals = append(globals, kiroStageResolvedGlobal{start: global.Start + adjustment, end: global.End + adjustment, name: global.Name})
			previousGlobal = global.End
		}
		start := starts[i]
		edits = append(edits, kiroStageSourceEdit{start: analysis.Tokens[start+hook.First].Start, end: analysis.Tokens[start+hook.Last-1].End, after: output.String(), captures: captures, globals: globals})
	}
	return edits, nil
}

// 不能以名字相同代替绑定保持：capture 绑定到原版声明，新声明必须使用保留前缀，
// 自由引用只能来自 profile 登记的 globals。编辑区间与有序 token 归并，避免每个编辑扫描全部 token。
func verifyKiroStageInjectedBindings(installed *kiroStageJSAnalysis, edits []kiroStageSourceEdit) error {
	tokens := installed.Tokens
	editBases := make([]int, len(edits))
	editEnds := make([]int, len(edits))
	capturesByStart := make(map[int]kiroStageResolvedCapture)
	globalsByStart := make(map[int]kiroStageResolvedGlobal)
	delta, previousEnd := 0, 0
	for i, edit := range edits {
		if edit.start < previousEnd || edit.end < edit.start {
			return fmt.Errorf("Kiro 注入编辑区间无效或重叠")
		}
		base := edit.start + delta
		editBases[i], editEnds[i] = base, base+len(edit.after)
		for _, capture := range edit.captures {
			position := base + capture.start
			if capture.start < 0 || capture.end <= capture.start || capture.end > len(edit.after) {
				return fmt.Errorf("Kiro 注入 capture 区间无效 at %d", position)
			}
			if _, exists := capturesByStart[position]; exists {
				return fmt.Errorf("Kiro 注入 capture 起点重复 at %d", position)
			}
			capturesByStart[position] = capture
		}
		for _, global := range edit.globals {
			position := base + global.start
			if global.start < 0 || global.end <= global.start || global.end > len(edit.after) || global.name == "" {
				return fmt.Errorf("Kiro 注入 globals 区间无效 at %d", position)
			}
			if _, exists := globalsByStart[position]; exists {
				return fmt.Errorf("Kiro 注入 globals 起点重复 at %d", position)
			}
			globalsByStart[position] = kiroStageResolvedGlobal{start: position, end: base + global.end, name: global.name}
		}
		delta += len(edit.after) - (edit.end - edit.start)
		previousEnd = edit.end
	}

	capturedDeclarations := make(map[int]int)
	reservedBindings := make(map[string]int)
	tokenIndex := 0
	for i := range edits {
		base, end := editBases[i], editEnds[i]
		for tokenIndex < len(tokens) && tokens[tokenIndex].Start < base {
			tokenIndex++
		}
		for tokenIndex < len(tokens) && tokens[tokenIndex].Start < end {
			token := tokens[tokenIndex]
			if token.Start < base || token.End > end {
				return fmt.Errorf("Kiro 注入 token 越过编辑边界 at %d", token.Start)
			}
			if token.Reference && token.Name != "" && token.Binding != "" && token.Declaration == token.Start {
				if capture, ok := capturesByStart[token.Start]; ok && capture.declaration {
					if token.End != base+capture.end {
						return fmt.Errorf("Kiro 注入 capture 声明区间不符 at %d", token.Start)
					}
					if prior, exists := capturedDeclarations[capture.originalDeclaration]; exists && prior != token.Start {
						return fmt.Errorf("Kiro 注入声明映射歧义: 原版 %d，候选 %d/%d (%s)", capture.originalDeclaration, prior, token.Start, token.Name)
					}
					capturedDeclarations[capture.originalDeclaration] = token.Start
				} else {
					if !strings.HasPrefix(token.Name, "kiroProxy") {
						return fmt.Errorf("Kiro 注入新声明未使用保留前缀: %q", token.Name)
					}
					if prior, exists := reservedBindings[token.Binding]; exists && prior != token.Declaration {
						return fmt.Errorf("Kiro 注入保留声明绑定不一致: %q", token.Name)
					}
					reservedBindings[token.Binding] = token.Declaration
				}
			}
			tokenIndex++
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
	findToken := func(position int) (kiroStageJSToken, bool) {
		index := sort.Search(len(tokens), func(i int) bool { return tokens[i].Start >= position })
		if index == len(tokens) || tokens[index].Start != position {
			return kiroStageJSToken{}, false
		}
		return tokens[index], true
	}
	for i, edit := range edits {
		base := editBases[i]
		for _, capture := range edit.captures {
			position := base + capture.start
			token, ok := findToken(position)
			expected, mapped := mapDeclaration(capture.originalDeclaration)
			if !ok || !mapped || token.End != base+capture.end || !token.Reference || token.Binding == "" || token.Declaration != expected || (token.Declaration == token.Start) != capture.declaration {
				return fmt.Errorf("Kiro 注入引用绑定复验失败 at %d", position)
			}
		}
	}

	matchedGlobals := make(map[int]bool, len(globalsByStart))
	tokenIndex = 0
	for i := range edits {
		base, end := editBases[i], editEnds[i]
		for tokenIndex < len(tokens) && tokens[tokenIndex].Start < base {
			tokenIndex++
		}
		for tokenIndex < len(tokens) && tokens[tokenIndex].Start < end {
			token := tokens[tokenIndex]
			if token.Reference {
				if capture, ok := capturesByStart[token.Start]; ok {
					if token.End != base+capture.end {
						return fmt.Errorf("Kiro 注入 capture token 区间不符 at %d", token.Start)
					}
				} else if token.Binding != "" {
					declaration, ok := reservedBindings[token.Binding]
					if !ok || token.Declaration != declaration {
						return fmt.Errorf("Kiro 注入标识符未归属 capture 或保留声明: %q at %d", token.Name, token.Start)
					}
				} else {
					global, ok := globalsByStart[token.Start]
					if !ok || token.End != global.end || token.Name != global.name || token.Key != "I:"+global.name || token.Binding != "" || token.Declaration != -1 {
						return fmt.Errorf("Kiro 注入自由引用未由 globals 精确登记: %q at %d", token.Name, token.Start)
					}
					matchedGlobals[token.Start] = true
				}
			}
			tokenIndex++
		}
	}
	if len(matchedGlobals) != len(globalsByStart) {
		for start, global := range globalsByStart {
			if !matchedGlobals[start] {
				return fmt.Errorf("Kiro 注入 globals 源引用未匹配: %q at %d", global.name, start)
			}
		}
	}
	return nil
}

type kiroStageContextIndex struct {
	positions map[string][]int
}

func buildKiroStageContextIndex(tokens []kiroStageJSToken) *kiroStageContextIndex {
	index := &kiroStageContextIndex{positions: make(map[string][]int)}
	for i, token := range tokens {
		index.positions[token.Key] = append(index.positions[token.Key], i)
	}
	return index
}

func findKiroStageContextIndexed(tokens []kiroStageJSToken, index *kiroStageContextIndex, pattern []kiroStagePatternToken) []int {
	if len(pattern) == 0 || index == nil {
		return nil
	}
	anchor, positions := -1, []int(nil)
	for i, token := range pattern {
		candidates := index.positions[token.Key]
		if len(candidates) == 0 {
			return nil
		}
		if positions == nil || len(candidates) < len(positions) {
			anchor, positions = i, candidates
		}
	}
	if anchor < 0 {
		return nil
	}
	var matches []int
	for _, candidate := range positions {
		start := candidate - anchor
		if start < 0 || start+len(pattern) > len(tokens) {
			continue
		}
		valid := true
		for i, expected := range pattern {
			actual := tokens[start+i]
			if actual.Key != expected.Key || actual.Shape != expected.Shape {
				valid = false
				break
			}
		}
		if !valid {
			continue
		}
		forward, reverse := map[string]string{}, map[string]string{}
		for i, expected := range pattern {
			if expected.Binding == "" {
				continue
			}
			binding := tokens[start+i].Binding
			if binding == "" {
				valid = false
				break
			}
			if prior, exists := forward[expected.Binding]; exists && prior != binding {
				valid = false
				break
			}
			if prior, exists := reverse[binding]; exists && prior != expected.Binding {
				valid = false
				break
			}
			forward[expected.Binding], reverse[binding] = binding, expected.Binding
		}
		if valid {
			matches = append(matches, start)
		}
	}
	return matches
}

// KMP 作为测试 oracle 与小输入兼容路径保留；不是字符串变量名替换。
func findKiroStageContext(tokens []kiroStageJSToken, pattern []kiroStagePatternToken) []int {
	if len(pattern) == 0 {
		return nil
	}
	failure := make([]int, len(pattern))
	for i, j := 1, 0; i < len(pattern); i++ {
		for j > 0 && (pattern[i].Key != pattern[j].Key || pattern[i].Shape != pattern[j].Shape) {
			j = failure[j-1]
		}
		if pattern[i].Key == pattern[j].Key && pattern[i].Shape == pattern[j].Shape {
			j++
		}
		failure[i] = j
	}
	var matches []int
	for i, j := 0, 0; i < len(tokens); i++ {
		for j > 0 && (tokens[i].Key != pattern[j].Key || tokens[i].Shape != pattern[j].Shape) {
			j = failure[j-1]
		}
		if tokens[i].Key == pattern[j].Key && tokens[i].Shape == pattern[j].Shape {
			j++
		}
		if j == len(pattern) {
			start := i - j + 1
			forward, reverse := map[string]string{}, map[string]string{}
			valid := true
			for k, p := range pattern {
				if p.Binding == "" {
					continue
				}
				b := tokens[start+k].Binding
				if b == "" {
					valid = false
					break
				}
				if old, ok := forward[p.Binding]; ok && old != b {
					valid = false
					break
				}
				if old, ok := reverse[b]; ok && old != p.Binding {
					valid = false
					break
				}
				forward[p.Binding], reverse[b] = b, p.Binding
			}
			if valid {
				matches = append(matches, start)
			}
			j = failure[j-1]
		}
	}
	return matches
}
