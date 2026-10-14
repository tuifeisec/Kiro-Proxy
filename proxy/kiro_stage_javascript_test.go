package proxy

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/tdewolff/parse/v2"
	"github.com/tdewolff/parse/v2/js"
)

// 真实脚本以环境变量传入，始终只读。
func TestKiroStageJSParserRealBundles(t *testing.T) {
	for _, env := range []string{"KIRO_STAGE_TEST_BUNDLE", "KIRO_STAGE_TEST_BUNDLE_130"} {
		t.Run(env, func(t *testing.T) {
			path := os.Getenv(env)
			if path == "" {
				t.Skip(env + " is required")
			}
			source, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			program, err := js.Parse(parse.NewInputBytes(source), js.Options{})
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("原生 tdewolff 解析成功: %s, %d bytes, %d statements", path, len(source), len(program.List))
		})
	}
}

func TestKiroStageJSAnalysisRealBundles(t *testing.T) {
	for _, env := range []string{"KIRO_STAGE_TEST_BUNDLE", "KIRO_STAGE_TEST_BUNDLE_130"} {
		t.Run(env, func(t *testing.T) {
			path := os.Getenv(env)
			if path == "" {
				t.Skip(env + " is required")
			}
			source, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			before := append([]byte(nil), source...)
			analysis, err := analyzeKiroStageJS(source)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(source, before) {
				t.Fatal("分析修改了源字节")
			}
			t.Logf("原生完整分析成功: %d bytes, %d tokens, %d nodes", len(source), len(analysis.Tokens), len(analysis.Nodes))
		})
	}
}

// 用最终 profiles 验证真实 bundle 的 token、Shape 和绑定均可命中结构计划。
func TestKiroStageJSRealProfileGeneration(t *testing.T) {
	profiles, err := loadKiroStageStructuralProfiles()
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct{ env, manifest, family string }{
		{"KIRO_STAGE_TEST_BUNDLE", "kiro_stage_bridge_manifest.json", "legacy-workflow"},
		{"KIRO_STAGE_TEST_BUNDLE_130", "kiro_stage_bridge_130_manifest.json", "native-model-routing"},
	} {
		t.Run(item.family, func(t *testing.T) {
			path := os.Getenv(item.env)
			if path == "" {
				t.Skip(item.env + " is required")
			}
			source, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			manifest, err := readKiroStageManifest(item.manifest)
			if err != nil {
				t.Fatal(err)
			}
			if kiroStageSHA256(source) != manifest.OriginalSHA256 {
				t.Fatal("golden 摘要不符")
			}
			analysis, err := analyzeKiroStageJS(source)
			if err != nil {
				t.Fatal(err)
			}
			var profile *kiroStageStructureProfile
			for i := range profiles {
				if profiles[i].Family == item.family {
					profile = &profiles[i]
					break
				}
			}
			if profile == nil {
				t.Fatalf("最终 profiles 缺少 family %q", item.family)
			}
			edits, err := planKiroStageStructure(analysis, *profile)
			if err != nil {
				t.Fatal(err)
			}
			if len(edits) == 0 {
				t.Fatal("真实 bundle 未生成结构编辑")
			}
			t.Logf("最终 profile 计划通过: %s, %d bytes, %d tokens, %d nodes, %d edits", item.family, len(source), len(analysis.Tokens), len(analysis.Nodes), len(edits))
		})
	}
}

func TestKiroStageJSNativeSourceCorrespondence(t *testing.T) {
	for _, source := range []string{
		`kA(new Map()[Symbol.iterator]());new X;new X();new X(1);`,
		`function f(a){var b=a;{let a=b;(()=>a)()}return()=>b}`,
		`const f=async({key:a},...r)=>{for await(let x of r){await x(a)}};`,
		`let a=1;const o={a,key:a,[a]:a,method(a){return a},get value(){return a}};`,
		`let {key:a,b=1,...r}=input;let [x,,y=2,...z]=input;`,
		`const t=tag` + "`raw // /* ${a} after ${()=>b} tail`" + `;const r=/[a/]+/gi;let n=0xA;`,
		`for(;;);for(let i=0;i<2;i++)i++;for(var x in o){x}switch(a){case 1:case 2:x();break;default:}`,
		`label:while(a){try{throw a}catch({message:e}){(()=>e)();break label}finally{a()}}`,
		`class A extends Base{static x=1;#x=2;method(a){return this.#x+a}static{var z=1;z++}}`,
		`function f(){return` + "\n" + `a};const x=(a)=>a;const y=()=>({a:1});`,
	} {
		t.Run(fmt.Sprintf("%.55s", source), func(t *testing.T) {
			analysis, err := analyzeKiroStageJS([]byte(source))
			if err != nil {
				t.Fatal(err)
			}
			last := 0
			for _, token := range analysis.Tokens {
				if token.Start < last || token.End < token.Start || token.End > len(source) {
					t.Fatalf("token 范围无效: %+v", token)
				}
				last = token.End
				if token.Key == "V" && (token.Binding == "" || token.Declaration < 0) {
					t.Fatalf("绑定缺失: %+v", token)
				}
			}
			for _, node := range analysis.Nodes {
				if node.Start < 0 || node.End <= node.Start || node.End > len(source) {
					t.Fatalf("node 范围无效: %+v", node)
				}
			}
			if len(analysis.Nodes) == 0 || len(analysis.Tokens) == 0 {
				t.Fatal("分析为空")
			}
		})
	}
}

func TestKiroStageJSShapeASIAndOperators(t *testing.T) {
	shapeAt := func(source, text string) string {
		t.Helper()
		analysis, err := analyzeKiroStageJS([]byte(source))
		if err != nil {
			t.Fatal(err)
		}
		start := strings.Index(source, text)
		if start < 0 {
			t.Fatalf("token %q missing in %q", text, source)
		}
		for _, token := range analysis.Tokens {
			if token.Start == start {
				return token.Shape
			}
		}
		t.Fatalf("token %q has no analysis token", text)
		return ""
	}
	shapeAtEnd := func(source, text string) string {
		t.Helper()
		analysis, err := analyzeKiroStageJS([]byte(source))
		if err != nil {
			t.Fatal(err)
		}
		start := strings.Index(source, text)
		if start < 0 {
			t.Fatalf("token %q missing in %q", text, source)
		}
		end := start + len(text) - 1
		for _, token := range analysis.Tokens {
			if token.Start == end {
				return token.Shape
			}
		}
		t.Fatalf("token %q has no analysis token", text)
		return ""
	}
	assertDifferent := func(label, left, right, token string, atEnd bool) {
		t.Helper()
		leftShape, rightShape := shapeAt(left, token), shapeAt(right, token)
		if atEnd {
			leftShape, rightShape = shapeAtEnd(left, token), shapeAtEnd(right, token)
		}
		if leftShape == rightShape {
			t.Fatalf("%s did not change AST Shape: %q vs %q", label, leftShape, rightShape)
		}
	}

	assertDifferent("return ASI", "function f(){return value}", "function f(){return\nvalue}", "return", true)
	assertDifferent("return comment newline ASI", "function f(){return value}", "function f(){return/*\n*/value}", "return", true)
	assertDifferent("break ASI", "function f(){target:while(true){break target;}}", "function f(){target:while(true){break\ntarget;}}", "break", true)
	assertDifferent("break comment newline ASI", "function f(){target:while(true){break target;}}", "function f(){target:while(true){break/*\n*/target;}}", "break", true)
	assertDifferent("continue ASI", "function f(){target:while(true){continue target;}}", "function f(){target:while(true){continue\ntarget;}}", "continue", true)
	assertDifferent("continue comment newline ASI", "function f(){target:while(true){continue target;}}", "function f(){target:while(true){continue/*\n*/target;}}", "continue", true)
	assertDifferent("binary operator", "const result=left+right;", "const result=left-right;", "left", false)
	if shape := shapeAt("const result=left+right;", "left"); !strings.Contains(shape, "BinaryExpr|Op=") {
		t.Fatalf("binary operator shape missing: %q", shape)
	}
	if shape := shapeAt("function f(){return target()} ", "target"); !strings.Contains(shape, "CallExpr|Prec=18|Optional=false;") {
		t.Fatalf("call precedence shape missing: %q", shape)
	}
}

func TestKiroStageJSUnboundIdentifierSentinel(t *testing.T) {
	analysis, err := analyzeKiroStageJS([]byte("function f(){return externalValue}"))
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range analysis.Tokens {
		if token.Name == "externalValue" {
			if token.Key != "I:externalValue" || token.Binding != "" || token.Declaration != -1 {
				t.Fatalf("free identifier has invalid binding sentinel: %+v", token)
			}
			return
		}
	}
	t.Fatal("free identifier token missing")
}

func TestKiroStageJSReferenceClassification(t *testing.T) {
	source := "function f(){return target.value+value}"
	analysis, err := analyzeKiroStageJS([]byte(source))
	if err != nil {
		t.Fatal(err)
	}
	findAt := func(start int) kiroStageJSToken {
		t.Helper()
		for _, token := range analysis.Tokens {
			if token.Start == start {
				return token
			}
		}
		t.Fatalf("token at %d missing", start)
		return kiroStageJSToken{}
	}
	property := findAt(strings.Index(source, ".value") + 1)
	freeReference := findAt(strings.LastIndex(source, "value"))
	if property.Name != "value" || property.Key != "I:value" || property.Reference || property.Binding != "" || property.Declaration != -1 {
		t.Fatalf("fixed property classification changed: %+v", property)
	}
	if freeReference.Name != "value" || freeReference.Key != property.Key || !freeReference.Reference || freeReference.Binding != "" || freeReference.Declaration != -1 {
		t.Fatalf("free reference classification changed: %+v", freeReference)
	}
	if property.Name != freeReference.Name || property.Binding != freeReference.Binding || property.Declaration != freeReference.Declaration {
		t.Fatalf("fixture must give property and free reference identical legacy fields: property=%+v reference=%+v", property, freeReference)
	}

	declarationSource := "const declaredValue=0"
	declarationAnalysis, err := analyzeKiroStageJS([]byte(declarationSource))
	if err != nil {
		t.Fatal(err)
	}
	declarationStart := strings.Index(declarationSource, "declaredValue")
	for _, token := range declarationAnalysis.Tokens {
		if token.Start == declarationStart {
			if !token.Reference || token.Declaration != declarationStart || token.Binding == "" {
				t.Fatalf("declaration resolve classification changed: %+v", token)
			}
			return
		}
	}
	t.Fatal("declaration token missing")
}

func TestKiroStageJSSyntaxFailureAndNoExecution(t *testing.T) {
	for _, source := range []string{`const x=;`, `async function f(){for await(}`, `const x="unterminated`} {
		input := []byte(source)
		before := append([]byte(nil), input...)
		if a, err := analyzeKiroStageJS(input); a != nil || err == nil {
			t.Fatalf("无效语法被接受: %q", source)
		}
		if !bytes.Equal(input, before) {
			t.Fatal("失败路径修改了输入")
		}
	}
	if err := validateKiroStageJS([]byte(`throw new Error("must never execute");for(;;){};require("node:fs").unlinkSync("never");`)); err != nil {
		t.Fatal(err)
	}
	if _, err := analyzeKiroStageJS([]byte(`with(obj){x}`)); err == nil || !strings.Contains(err.Error(), "动态作用域") {
		t.Fatalf("动态作用域未明确拒绝: %v", err)
	}
}

func TestKiroStageJSIndependentBindingAssertions(t *testing.T) {
	type expected struct{ label, group, key, declaration string }
	tests := []struct {
		name, source string
		occurrences  []expected
	}{
		{"hoist shadow closure", "function f(/*p*/x){/*pre*/y;var /*pv*/x;var /*v*/y;{var /*v2*/y;let /*inner*/x=y;(()=>/*iu*/x)()}return()=>[/*pu*/x,/*vu*/y]}",
			[]expected{{"p", "p", "V", "p"}, {"pv", "p", "V", "p"}, {"pre", "v", "V", "v"}, {"v", "v", "V", "v"}, {"v2", "v", "V", "v"}, {"inner", "inner", "V", "inner"}, {"iu", "inner", "V", "inner"}, {"pu", "p", "V", "p"}, {"vu", "v", "V", "v"}}},
		{"default parameters", "let /*outer*/b=7;function f(a=()=>/*ou*/b){var /*body*/b=1;return /*bu*/b};function g(a=/*later*/b,/*bp*/b=1){return /*bpu*/b}",
			[]expected{{"outer", "outer", "V", "outer"}, {"ou", "outer", "V", "outer"}, {"body", "body", "V", "body"}, {"bu", "body", "V", "body"}, {"later", "bp", "V", "bp"}, {"bp", "bp", "V", "bp"}, {"bpu", "bp", "V", "bp"}}},
		{"named expression body var", "let /*root*/x=1;const f=function /*self*/x(){var /*local*/x;return()=>/*lu*/x};/*ru*/x;const g=function /*gself*/g(){return()=>/*gu*/g}",
			[]expected{{"root", "root", "V", "root"}, {"self", "self", "V", "self"}, {"local", "local", "V", "local"}, {"lu", "local", "V", "local"}, {"ru", "root", "V", "root"}, {"gself", "g", "V", "gself"}, {"gu", "g", "V", "gself"}}},
		{"catch shorthand properties", "let /*outer*/x=1;try{throw x}catch(/*catch*/x){(()=>/*cu*/x)()}const o={/*short*/x,/*key*/x:/*value*/x,method(/*param*/x){return /*pu*/x}};o./*dot*/x;/*JSON*/JSON;/*process*/process;/*require*/require",
			[]expected{{"outer", "outer", "V", "outer"}, {"catch", "catch", "V", "catch"}, {"cu", "catch", "V", "catch"}, {"short", "outer", "I:x", "outer"}, {"key", "", "I:x", ""}, {"value", "outer", "V", "outer"}, {"param", "p", "V", "param"}, {"pu", "p", "V", "param"}, {"dot", "", "I:x", ""}, {"JSON", "", "I:JSON", ""}, {"process", "", "I:process", ""}, {"require", "", "I:require", ""}}},
		{"rest var and unicode", "function f(/*p*/x,.../*r*/r){var /*pv*/x;var /*rv*/r;return [/*pu*/x,/*ru*/r]}let /*a*/\\u0061=1;/*au*/a;let /*wide*/\\u{1d400}=2;/*wu*/𝐀;",
			[]expected{{"p", "p", "V", "p"}, {"pv", "p", "V", "p"}, {"pu", "p", "V", "p"}, {"r", "r", "V", "r"}, {"rv", "r", "V", "r"}, {"ru", "r", "V", "r"}, {"a", "a", "V", "a"}, {"au", "a", "V", "a"}, {"wide", "wide", "V", "wide"}, {"wu", "wide", "V", "wide"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			analysis, err := analyzeKiroStageJS([]byte(test.source))
			if err != nil {
				t.Fatal(err)
			}
			offset := func(label string) int {
				marker := "/*" + label + "*/"
				i := strings.Index(test.source, marker)
				if i < 0 {
					t.Fatalf("missing %s", marker)
				}
				return i + len(marker)
			}
			byStart := map[int]kiroStageJSToken{}
			for _, token := range analysis.Tokens {
				byStart[token.Start] = token
			}
			groups, owners := map[string]string{}, map[string]string{}
			for _, e := range test.occurrences {
				token, found := byStart[offset(e.label)]
				if !found || token.Key != e.key {
					t.Fatalf("%s: %+v expected %s", e.label, token, e.key)
				}
				if e.group == "" {
					if token.Binding != "" || token.Declaration != -1 {
						t.Fatalf("固定角色绑定被泛化: %+v", token)
					}
					continue
				}
				if token.Binding == "" || token.Declaration != offset(e.declaration) {
					t.Fatalf("%s: %+v expected definition at %d", e.label, token, offset(e.declaration))
				}
				if b := groups[e.group]; b != "" && b != token.Binding {
					t.Fatalf("同一绑定不一致: %s", e.group)
				}
				if owner := owners[token.Binding]; owner != "" && owner != e.group {
					t.Fatalf("不同绑定被合并: %s and %s", owner, e.group)
				}
				groups[e.group] = token.Binding
				owners[token.Binding] = e.group
			}
		})
	}
}

func TestKiroStageJSIndependentAtomicRanges(t *testing.T) {
	source := "let a='x\\u0061';/* hidden */a=\"xa\";let r=/a\\/[/*]+/gi;let n=0x10;function f(){return a}\n//# sourceMappingURL=https://invalid.example/never.map"
	analysis, err := analyzeKiroStageJS([]byte(source))
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, token := range analysis.Tokens {
		counts[token.Key]++
		if token.Name == "hidden" {
			t.Fatal("注释被 token 化")
		}
	}
	for key, count := range map[string]int{"S:xa": 2, "R:/a\\/[/*]+/gi": 1, "N:0x10": 1} {
		if counts[key] != count {
			t.Fatalf("%s count %d expected %d", key, counts[key], count)
		}
	}
	start := strings.Index(source, "function f")
	end := strings.Index(source[start:], "}") + start + 1
	seen := map[kiroStageJSRange]bool{}
	found := false
	for _, node := range analysis.Nodes {
		if seen[node] {
			t.Fatalf("重复范围: %+v", node)
		}
		seen[node] = true
		if node.Start == start && node.End == end {
			found = true
		}
	}
	if !found {
		t.Fatalf("完整函数范围缺失: %d..%d", start, end)
	}
}
