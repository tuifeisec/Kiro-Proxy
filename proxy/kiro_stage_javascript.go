package proxy

import (
	"bytes"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/tdewolff/parse/v2"
	"github.com/tdewolff/parse/v2/js"
)

// 所有位置均为原始 UTF-8 字节偏移，左闭右开。
type kiroStageJSRange struct{ Start, End int }

type kiroStageJSToken struct {
	Start, End         int
	Key, Name, Binding string
	Reference          bool
	Shape              string
	Declaration        int
}

type kiroStageJSAnalysis struct {
	Tokens []kiroStageJSToken
	Nodes  []kiroStageJSRange
}

func parseKiroStageJSInput(input *parse.Input) (*js.AST, error) {
	program, err := js.Parse(input, js.Options{})
	if err != nil {
		return nil, fmt.Errorf("Kiro JavaScript 解析失败: %w", err)
	}
	return program, nil
}

// 解析输入的独立副本，避免官方 Input 添加 NULL 哨兵时触及调用者的底层数组。
func parseKiroStageJS(source []byte) (*js.AST, error) {
	input := parse.NewInputBytes(append([]byte(nil), source...))
	defer input.Restore()
	return parseKiroStageJSInput(input)
}

// validateKiroStageJS 只做完整原生语法解析，不构造 token 或作用域分析结果。
func validateKiroStageJS(source []byte) error {
	_, err := parseKiroStageJS(source)
	return err
}

func analyzeKiroStageJS(source []byte) (*kiroStageJSAnalysis, error) {
	program, err := parseKiroStageJS(source)
	if err != nil {
		return nil, err
	}
	input := parse.NewInputBytes(append([]byte(nil), source...))
	defer input.Restore()
	roles := &kiroStageJSRoleVisitor{parents: make([]js.INode, 0, 32), expressions: make(map[js.INode]bool)}
	js.Walk(roles, program)
	w := &kiroStageJSWalker{
		input:       input,
		lexer:       js.NewLexer(input),
		ranges:      make(map[kiroStageJSRange]bool),
		expressions: roles.expressions,
		shapeFields: make(map[reflect.Type][]kiroStageJSShapeField),
		shapeStack:  make([]kiroStageJSShapeFrame, 0, 64),
	}
	// 官方 Parser 特殊处理 shebang；位置恢复也仅跳过该首行注释，原文不变。
	if len(source) > 1 && source[0] == '#' && source[1] == '!' {
		end := bytes.IndexAny(source, "\r\n")
		if end < 0 {
			end = len(source)
		}
		input.Move(end)
		input.Skip()
	}
	w.next()
	root := w.scope(nil, true)
	for _, statement := range program.List {
		w.walk(statement, root)
	}
	if w.err != nil {
		return nil, w.err
	}
	if w.tt != js.ErrorToken {
		return nil, fmt.Errorf("Kiro JavaScript AST 源对应未耗尽 at %d: %q", w.start, w.raw)
	}
	result := &kiroStageJSAnalysis{Tokens: make([]kiroStageJSToken, 0, len(w.tokens))}
	type resolveKey struct {
		scope *kiroStageJSScope
		name  string
	}
	type resolveResult struct {
		binding *kiroStageJSBinding
	}
	var resolved map[resolveKey]resolveResult
	for _, occurrence := range w.tokens {
		t := occurrence.token
		t.Reference = occurrence.resolve
		binding := occurrence.binding
		if binding == nil && occurrence.resolve {
			key := resolveKey{scope: occurrence.scope, name: t.Name}
			cached, ok := resolved[key]
			if ok {
				binding = cached.binding
			} else {
				for s := occurrence.scope; s != nil; s = s.parent {
					if binding = s.bindings[t.Name]; binding != nil {
						break
					}
				}
				if resolved == nil {
					resolved = make(map[resolveKey]resolveResult)
				}
				resolved[key] = resolveResult{binding: binding}
			}
		}
		if binding != nil {
			t.Binding = binding.id
			t.Declaration = binding.declaration
		} else if occurrence.resolve {
			t.Key = "I:" + t.Name
			t.Declaration = -1
		}
		result.Tokens = append(result.Tokens, t)
	}
	for span := range w.ranges {
		result.Nodes = append(result.Nodes, span)
	}
	sort.Slice(result.Nodes, func(i, j int) bool {
		a, b := result.Nodes[i], result.Nodes[j]
		if a.Start == b.Start {
			return a.End < b.End
		}
		return a.Start < b.Start
	})
	return result, nil
}

type kiroStageJSBinding struct {
	id          string
	declaration int
}
type kiroStageJSScope struct {
	parent, variable *kiroStageJSScope
	bindings         map[string]*kiroStageJSBinding
	id               int
}
type kiroStageJSOccurrence struct {
	token   kiroStageJSToken
	scope   *kiroStageJSScope
	binding *kiroStageJSBinding
	resolve bool
}
type kiroStageJSShapeField struct {
	name  string
	index int
	kind  reflect.Kind
}
type kiroStageJSShapeFrame struct {
	descriptor string
	firstToken int
}
type kiroStageJSWalker struct {
	input               *parse.Input
	lexer               *js.Lexer
	tt                  js.TokenType
	raw                 []byte
	start, end, lastEnd int
	lineBefore          bool
	tokens              []kiroStageJSOccurrence
	ranges              map[kiroStageJSRange]bool
	scopeCount          int
	err                 error
	expressions         map[js.INode]bool
	shapeFields         map[reflect.Type][]kiroStageJSShapeField
	shapeStack          []kiroStageJSShapeFrame
}

func kiroStageJSNumericKind(kind reflect.Kind) bool {
	switch kind {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return true
	default:
		return false
	}
}

func (w *kiroStageJSWalker) shapeDescriptor(node any) string {
	value := reflect.ValueOf(node)
	for value.IsValid() && value.Kind() == reflect.Ptr {
		if value.IsNil() {
			return ""
		}
		value = value.Elem()
	}
	if !value.IsValid() || value.Kind() != reflect.Struct {
		return ""
	}
	typ := value.Type()
	fields, cached := w.shapeFields[typ]
	if !cached {
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			if field.Type.Kind() == reflect.Bool ||
				(field.Type.Name() == "TokenType" || field.Type.Name() == "Op" || field.Type.Name() == "Prec" || field.Type.Name() == "OpPrec" || typ.Name() == "Var" && field.Name == "Decl") && kiroStageJSNumericKind(field.Type.Kind()) {
				fields = append(fields, kiroStageJSShapeField{name: field.Name, index: i, kind: field.Type.Kind()})
			}
		}
		w.shapeFields[typ] = fields
	}
	result := make([]byte, 0, len(typ.Name())+len(fields)*20)
	result = append(result, typ.Name()...)
	for _, field := range fields {
		result = append(result, '|')
		result = append(result, field.name...)
		result = append(result, '=')
		fieldValue := value.Field(field.index)
		if field.kind == reflect.Bool {
			if fieldValue.Bool() {
				result = append(result, "true"...)
			} else {
				result = append(result, "false"...)
			}
		} else if field.kind >= reflect.Int && field.kind <= reflect.Int64 {
			result = strconv.AppendInt(result, fieldValue.Int(), 10)
		} else {
			result = strconv.AppendUint(result, fieldValue.Uint(), 10)
		}
	}
	return string(result)
}

func kiroStageJSShapeEvent(kind byte, descriptor string) string {
	result := make([]byte, 0, len(descriptor)+16)
	result = append(result, kind)
	result = strconv.AppendInt(result, int64(len(descriptor)), 10)
	result = append(result, ':')
	result = append(result, descriptor...)
	result = append(result, ';')
	return string(result)
}

func (w *kiroStageJSWalker) enterShape(node any) int {
	descriptor := w.shapeDescriptor(node)
	if descriptor == "" {
		return -1
	}
	index := len(w.shapeStack)
	w.shapeStack = append(w.shapeStack, kiroStageJSShapeFrame{descriptor: descriptor, firstToken: -1})
	return index
}

func (w *kiroStageJSWalker) leaveShape(index int) {
	if index < 0 || index >= len(w.shapeStack) {
		return
	}
	frame := w.shapeStack[index]
	if frame.firstToken >= 0 && len(w.tokens) > frame.firstToken {
		last := &w.tokens[len(w.tokens)-1].token
		last.Shape += kiroStageJSShapeEvent('C', frame.descriptor)
	}
	w.shapeStack = w.shapeStack[:index]
}

// Var.Decl 可被后续同名 var 改写；声明角色由 AST 父字段决定。
// 官方 Walk 不访问 Scope/Var.Link 元数据，不反射遍历树。
type kiroStageJSRoleVisitor struct {
	parents     []js.INode
	expressions map[js.INode]bool
}

func (v *kiroStageJSRoleVisitor) Enter(n js.INode) js.IVisitor {
	switch n.(type) {
	case *js.FuncDecl, *js.ClassDecl:
		declaration := false
		if len(v.parents) > 0 {
			switch parent := v.parents[len(v.parents)-1].(type) {
			case *js.AST, *js.BlockStmt:
				declaration = true
			case *js.IfStmt:
				declaration = parent.Body == n || parent.Else == n
			case *js.WhileStmt:
				declaration = parent.Body == n
			case *js.DoWhileStmt:
				declaration = parent.Body == n
			case *js.LabelledStmt:
				declaration = parent.Value == n
			case *js.CaseClause:
				declaration = parent.Cond != n
			}
		}
		v.expressions[n] = !declaration
	}
	v.parents = append(v.parents, n)
	return v
}
func (v *kiroStageJSRoleVisitor) Exit(js.INode) { v.parents = v.parents[:len(v.parents)-1] }

func (w *kiroStageJSWalker) scope(parent *kiroStageJSScope, variable bool) *kiroStageJSScope {
	w.scopeCount++
	s := &kiroStageJSScope{parent: parent, id: w.scopeCount}
	if parent == nil || variable {
		s.variable = s
	} else {
		s.variable = parent.variable
	}
	return s
}
func (w *kiroStageJSWalker) declare(name string, offset int, s *kiroStageJSScope) *kiroStageJSBinding {
	if b := s.bindings[name]; b != nil {
		if offset < b.declaration {
			b.declaration = offset
		}
		return b
	}
	b := &kiroStageJSBinding{id: fmt.Sprintf("b:%d:%s", s.id, name), declaration: offset}
	if s.bindings == nil {
		s.bindings = make(map[string]*kiroStageJSBinding)
	}
	s.bindings[name] = b
	return b
}
func (w *kiroStageJSWalker) next() {
	if w.err != nil {
		return
	}
	w.lineBefore = false
	for {
		w.tt, w.raw = w.lexer.Next()
		w.end = w.input.Offset()
		w.start = w.end - len(w.raw)
		switch w.tt {
		case js.WhitespaceToken, js.CommentToken:
			continue
		case js.LineTerminatorToken, js.CommentLineTerminatorToken:
			w.lineBefore = true
			continue
		case js.ErrorToken:
			if err := w.lexer.Err(); err != io.EOF {
				w.err = fmt.Errorf("Kiro JavaScript 源 token 读取失败: %w", err)
			}
		}
		break
	}
}
func (w *kiroStageJSWalker) fail(expected string) {
	if w.err == nil {
		w.err = fmt.Errorf("Kiro JavaScript AST 源对应失败 at %d: expected %s, got %q (%s)", w.start, expected, w.raw, w.tt)
	}
}
func (w *kiroStageJSWalker) span(start int) {
	if start >= 0 && w.lastEnd > start && w.lastEnd <= w.input.Len() {
		w.ranges[kiroStageJSRange{start, w.lastEnd}] = true
	}
}
func (w *kiroStageJSWalker) appendToken(t kiroStageJSToken, s *kiroStageJSScope, b *kiroStageJSBinding, resolve bool) int {
	i := len(w.tokens)
	w.tokens = append(w.tokens, kiroStageJSOccurrence{token: t, scope: s, binding: b, resolve: resolve})
	firstUnstarted := len(w.shapeStack)
	for j := len(w.shapeStack) - 1; j >= 0; j-- {
		if w.shapeStack[j].firstToken >= 0 {
			break
		}
		w.shapeStack[j].firstToken = i
		firstUnstarted = j
	}
	for j := firstUnstarted; j < len(w.shapeStack); j++ {
		frame := &w.shapeStack[j]
		w.tokens[i].token.Shape += kiroStageJSShapeEvent('O', frame.descriptor)
	}
	return i
}
func (w *kiroStageJSWalker) punctuation() {
	if w.err != nil {
		return
	}
	for pos := w.start; pos < w.end; pos++ {
		w.appendToken(kiroStageJSToken{Start: pos, End: pos + 1, Key: "P:" + string(w.input.Bytes()[pos:pos+1]), Declaration: -1}, nil, nil, false)
	}
	w.lastEnd = w.end
	w.next()
}
func (w *kiroStageJSWalker) take(tt js.TokenType) {
	if w.err != nil {
		return
	}
	if w.tt != tt {
		w.fail(tt.String())
		return
	}
	w.punctuation()
}
func (w *kiroStageJSWalker) semi() {
	if w.tt == js.SemicolonToken && !w.lineBefore {
		w.take(js.SemicolonToken)
	}
}
func kiroStageJSNil(n js.INode) bool {
	if n == nil {
		return true
	}
	v := reflect.ValueOf(n)
	return v.Kind() == reflect.Ptr && v.IsNil()
}

// 解码官方 lexer 已定位的字符串/标识符原子，不执行 JavaScript，不自行识别 JS token。
// 单独 surrogate 使用 WTF-8 保留，避免不同 JS 字符串被 Go 的替代字符折叠。
func kiroStageJSDecode(raw []byte, quoted bool) (string, error) {
	if quoted {
		if len(raw) < 2 {
			return "", fmt.Errorf("无效字符串原子")
		}
		raw = raw[1 : len(raw)-1]
	}
	units := make([]uint16, 0, len(raw))
	appendRune := func(r rune) {
		if r > 0xffff {
			a, b := utf16.EncodeRune(r)
			units = append(units, uint16(a), uint16(b))
		} else {
			units = append(units, uint16(r))
		}
	}
	for i := 0; i < len(raw); {
		if raw[i] != '\\' {
			r, n := utf8.DecodeRune(raw[i:])
			appendRune(r)
			i += n
			continue
		}
		i++
		if i >= len(raw) {
			return "", fmt.Errorf("不完整转义原子")
		}
		c := raw[i]
		i++
		switch c {
		case '\n':
			continue
		case '\r':
			if i < len(raw) && raw[i] == '\n' {
				i++
			}
			continue
		case 'b':
			units = append(units, '\b')
		case 'f':
			units = append(units, '\f')
		case 'n':
			units = append(units, '\n')
		case 'r':
			units = append(units, '\r')
		case 't':
			units = append(units, '\t')
		case 'v':
			units = append(units, '\v')
		case 'x', 'u':
			digits := 4
			if c == 'x' {
				digits = 2
			}
			end := i + digits
			braced := c == 'u' && i < len(raw) && raw[i] == '{'
			if braced {
				i++
				j := bytes.IndexByte(raw[i:], '}')
				if j < 0 {
					return "", fmt.Errorf("不完整 Unicode 转义")
				}
				end = i + j
			}
			if end > len(raw) {
				return "", fmt.Errorf("不完整 Unicode 转义")
			}
			value, err := strconv.ParseUint(string(raw[i:end]), 16, 32)
			if err != nil || value > utf8.MaxRune {
				return "", fmt.Errorf("无效 Unicode 转义: %q", raw[i:end])
			}
			appendRune(rune(value))
			i = end
			if braced {
				i++
			}
		default:
			if c >= '0' && c <= '7' {
				value := int(c - '0')
				limit := 2
				if c > '3' {
					limit = 1
				}
				for j := 0; j < limit && i < len(raw) && raw[i] >= '0' && raw[i] <= '7'; j++ {
					value = value*8 + int(raw[i]-'0')
					i++
				}
				units = append(units, uint16(value))
			} else if c >= utf8.RuneSelf {
				r, n := utf8.DecodeRune(raw[i-1:])
				i += n - 1
				if r != '\u2028' && r != '\u2029' {
					appendRune(r)
				}
			} else {
				units = append(units, uint16(c))
			}
		}
	}
	var result strings.Builder
	for i := 0; i < len(units); i++ {
		u := units[i]
		if u >= 0xd800 && u <= 0xdbff && i+1 < len(units) && units[i+1] >= 0xdc00 && units[i+1] <= 0xdfff {
			result.WriteRune(utf16.DecodeRune(rune(u), rune(units[i+1])))
			i++
		} else if u >= 0xd800 && u <= 0xdfff {
			result.WriteByte(byte(0xe0 | u>>12))
			result.WriteByte(byte(0x80 | (u>>6)&63))
			result.WriteByte(byte(0x80 | u&63))
		} else {
			result.WriteRune(rune(u))
		}
	}
	return result.String(), nil
}

func kiroStageJSDecodeIdentifier(raw []byte) (string, error) {
	if bytes.IndexByte(raw, '\\') < 0 {
		return string(raw), nil
	}
	return kiroStageJSDecode(raw, false)
}

func (w *kiroStageJSWalker) identifier(data []byte, s, declaration *kiroStageJSScope, fixed, resolve bool) int {
	if w.err != nil {
		return -1
	}
	name, err := kiroStageJSDecodeIdentifier(w.raw)
	expected, expectedErr := name, err
	if !bytes.Equal(w.raw, data) {
		expected, expectedErr = kiroStageJSDecodeIdentifier(data)
	}
	if err != nil || expectedErr != nil || name != expected || (!js.IsIdentifierName(w.tt) && w.tt != js.PrivateIdentifierToken) {
		w.fail("identifier " + string(data))
		return -1
	}
	start := w.start
	var b *kiroStageJSBinding
	if declaration != nil {
		b = w.declare(name, start, declaration)
	}
	key := "V"
	if fixed {
		key = "I:" + name
	}
	index := w.appendToken(kiroStageJSToken{Start: start, End: w.end, Key: key, Name: name, Declaration: -1}, s, b, resolve)
	w.lastEnd = w.end
	w.next()
	w.span(start)
	return index
}
func (w *kiroStageJSWalker) atom(key string) int {
	start := w.start
	i := w.appendToken(kiroStageJSToken{Start: start, End: w.end, Key: key, Declaration: -1}, nil, nil, false)
	w.lastEnd = w.end
	w.next()
	w.span(start)
	return i
}
func (w *kiroStageJSWalker) literal(n *js.LiteralExpr, s *kiroStageJSScope, fixed bool) int {
	if w.err != nil {
		return -1
	}
	if n.TokenType == js.RegExpToken && (w.tt == js.DivToken || w.tt == js.DivEqToken) {
		w.tt, w.raw = w.lexer.RegExp()
		w.end = w.input.Offset()
		w.start = w.end - len(w.raw)
	}
	switch {
	case w.tt == js.StringToken:
		decoded, err := kiroStageJSDecode(w.raw, true)
		if err != nil {
			w.err = err
			return -1
		}
		return w.atom("S:" + decoded)
	case w.tt == js.RegExpToken:
		if !bytes.Equal(w.raw, n.Data) {
			w.fail("regular expression " + string(n.Data))
			return -1
		}
		return w.atom("R:" + string(w.raw))
	case js.IsNumeric(w.tt):
		return w.atom("N:" + string(w.raw))
	case fixed && js.IsIdentifierName(w.tt):
		return w.identifier(w.raw, s, nil, true, false)
	case !fixed:
		if !bytes.Equal(w.raw, n.Data) {
			w.fail("literal " + string(n.Data))
			return -1
		}
		w.punctuation()
		return -1
	default:
		w.fail("literal " + string(n.Data))
		return -1
	}
}
func (w *kiroStageJSWalker) property(name *js.PropertyName, s *kiroStageJSScope) int {
	start := w.start
	defer w.span(start)
	if name.Computed != nil {
		w.take(js.OpenBracketToken)
		w.walk(name.Computed, s)
		w.take(js.CloseBracketToken)
		return -1
	}
	return w.literal(&name.Literal, s, true)
}

func (w *kiroStageJSWalker) binding(n js.IBinding, s, declaration *kiroStageJSScope, fixed bool) bool {
	if kiroStageJSNil(n) || w.err != nil {
		return false
	}
	shape := w.enterShape(n)
	defer w.leaveShape(shape)
	start := w.start
	defer w.span(start)
	requiresSeparateScope := false
	switch n := n.(type) {
	case *js.Var:
		w.identifier(n.Data, s, declaration, fixed, true)
	case *js.BindingArray:
		w.take(js.OpenBracketToken)
		for i, item := range n.List {
			if i > 0 {
				w.take(js.CommaToken)
			}
			if w.bindingElement(item, s, declaration) {
				requiresSeparateScope = true
			}
		}
		if n.Rest != nil {
			if len(n.List) > 0 {
				w.take(js.CommaToken)
			}
			w.take(js.EllipsisToken)
			if w.binding(n.Rest, s, declaration, false) {
				requiresSeparateScope = true
			}
		}
		if w.tt == js.CommaToken {
			w.take(js.CommaToken)
		}
		w.take(js.CloseBracketToken)
	case *js.BindingObject:
		w.take(js.OpenBraceToken)
		for i, item := range n.List {
			if i > 0 {
				w.take(js.CommaToken)
			}
			itemStart := w.start
			if item.Key.Computed != nil {
				requiresSeparateScope = true
			}
			index := w.property(item.Key, s)
			if w.tt == js.ColonToken {
				w.take(js.ColonToken)
				if w.bindingElement(item.Value, s, declaration) {
					requiresSeparateScope = true
				}
			} else if v, ok := item.Value.Binding.(*js.Var); ok && index >= 0 {
				t := &w.tokens[index]
				expected, _ := kiroStageJSDecodeIdentifier(v.Data)
				if t.token.Name != expected {
					w.fail("shorthand " + expected)
					return false
				}
				t.resolve = true
				t.scope = s
				if declaration != nil {
					t.binding = w.declare(expected, t.token.Start, declaration)
				}
				if item.Value.Default != nil {
					requiresSeparateScope = true
					w.take(js.EqToken)
					w.walk(item.Value.Default, s)
				}
			} else {
				w.fail("binding property colon")
				return false
			}
			w.span(itemStart)
		}
		if n.Rest != nil {
			if len(n.List) > 0 {
				w.take(js.CommaToken)
			}
			w.take(js.EllipsisToken)
			if w.binding(n.Rest, s, declaration, false) {
				requiresSeparateScope = true
			}
		}
		if w.tt == js.CommaToken {
			w.take(js.CommaToken)
		}
		w.take(js.CloseBraceToken)
	default:
		w.fail(fmt.Sprintf("binding AST %T", n))
	}
	return requiresSeparateScope
}
func (w *kiroStageJSWalker) bindingElement(element js.BindingElement, s, declaration *kiroStageJSScope) bool {
	start := w.start
	requiresSeparateScope := w.binding(element.Binding, s, declaration, false)
	if element.Default != nil {
		requiresSeparateScope = true
		w.take(js.EqToken)
		w.walk(element.Default, s)
	}
	if element.Binding != nil {
		w.span(start)
	}
	return requiresSeparateScope
}
func (w *kiroStageJSWalker) parameters(params js.Params, s *kiroStageJSScope, paren bool) *kiroStageJSScope {
	start := w.start
	if paren {
		w.take(js.OpenParenToken)
	}
	separate := false
	for i, element := range params.List {
		if i > 0 {
			w.take(js.CommaToken)
		}
		if w.bindingElement(element, s, s) {
			separate = true
		}
	}
	if params.Rest != nil {
		if len(params.List) > 0 {
			w.take(js.CommaToken)
		}
		w.take(js.EllipsisToken)
		if w.binding(params.Rest, s, s, false) {
			separate = true
		}
	}
	if paren {
		if w.tt == js.CommaToken {
			w.take(js.CommaToken)
		}
		w.take(js.CloseParenToken)
	}
	w.span(start)
	if separate {
		return w.scope(s, true)
	}
	return s
}
func (w *kiroStageJSWalker) block(block *js.BlockStmt, s *kiroStageJSScope) {
	shape := w.enterShape(block)
	defer w.leaveShape(shape)
	start := w.start
	w.take(js.OpenBraceToken)
	scope := w.scope(s, false)
	for _, statement := range block.List {
		w.walk(statement, scope)
	}
	w.take(js.CloseBraceToken)
	w.span(start)
}
func (w *kiroStageJSWalker) loopBody(block *js.BlockStmt, s *kiroStageJSScope) {
	if w.tt == js.OpenBraceToken {
		w.block(block, s)
	} else if len(block.List) == 0 {
		w.take(js.SemicolonToken)
	} else {
		for _, statement := range block.List {
			w.walk(statement, s)
		}
	}
}
func (w *kiroStageJSWalker) method(n *js.MethodDecl, s *kiroStageJSScope) {
	shape := w.enterShape(n)
	defer w.leaveShape(shape)
	start := w.start
	if n.Static {
		w.take(js.StaticToken)
	}
	if n.Async {
		w.take(js.AsyncToken)
	}
	if n.Get {
		w.take(js.GetToken)
	}
	if n.Set {
		w.take(js.SetToken)
	}
	if n.Generator {
		w.take(js.MulToken)
	}
	w.className(n.Name, s)
	bodyScope := w.parameters(n.Params, w.scope(s, true), true)
	w.block(&n.Body, bodyScope)
	w.span(start)
}
func (w *kiroStageJSWalker) className(name js.ClassElementName, s *kiroStageJSScope) {
	if name.Private != nil {
		w.identifier(name.Private.Data, s, nil, true, false)
	} else {
		w.property(&name.PropertyName, s)
	}
}
func (w *kiroStageJSWalker) arguments(args *js.Args, s *kiroStageJSScope) {
	start := w.start
	w.take(js.OpenParenToken)
	for i, arg := range args.List {
		if i > 0 {
			w.take(js.CommaToken)
		}
		argStart := w.start
		if arg.Rest {
			w.take(js.EllipsisToken)
		}
		w.walk(arg.Value, s)
		w.span(argStart)
	}
	if w.tt == js.CommaToken {
		w.take(js.CommaToken)
	}
	w.take(js.CloseParenToken)
	w.span(start)
}
func (w *kiroStageJSWalker) template(raw []byte, tail bool) {
	if !bytes.Equal(w.raw, raw) {
		w.fail("template quasi")
		return
	}
	start, end := w.start, w.end
	w.appendToken(kiroStageJSToken{Start: start, End: start + 1, Key: "P:" + string(raw[:1]), Declaration: -1}, nil, nil, false)
	quasiEnd := end - 2
	if tail {
		quasiEnd = end - 1
	}
	w.appendToken(kiroStageJSToken{Start: start + 1, End: quasiEnd, Key: "T:" + string(w.input.Bytes()[start+1:quasiEnd]), Declaration: -1}, nil, nil, false)
	for pos := quasiEnd; pos < end; pos++ {
		w.appendToken(kiroStageJSToken{Start: pos, End: pos + 1, Key: "P:" + string(w.input.Bytes()[pos:pos+1]), Declaration: -1}, nil, nil, false)
	}
	if quasiEnd > start+1 {
		w.ranges[kiroStageJSRange{start + 1, quasiEnd}] = true
	}
	w.lastEnd = end
	w.next()
}

func (w *kiroStageJSWalker) walk(n js.INode, s *kiroStageJSScope) {
	if kiroStageJSNil(n) || w.err != nil {
		return
	}
	if _, ok := n.(*js.Comment); ok {
		return
	} // 官方 AST 将保留注释前置，lexer 已按原位置忽略。
	if v, ok := n.(*js.VarDecl); ok && len(v.List) == 0 {
		return
	} // for(;;) 的合成空声明。
	shape := -1
	switch n.(type) {
	case *js.BlockStmt, *js.MethodDecl:
		// block/method 也由其他 AST 路径直接调用，由对应 helper 统一记录。
	default:
		shape = w.enterShape(n)
	}
	if shape >= 0 {
		defer w.leaveShape(shape)
	}
	start := w.start
	defer w.span(start)
	switch n := n.(type) {
	case *js.Var:
		w.identifier(n.Data, s, nil, false, true)
	case *js.LiteralExpr:
		w.literal(n, s, false)
	case js.LiteralExpr:
		w.literal(&n, s, false)
	case *js.BlockStmt:
		w.block(n, s)
		w.semi()
	case *js.EmptyStmt:
		w.take(js.SemicolonToken)
	case *js.ExprStmt:
		w.walk(n.Value, s)
		w.semi()
	case *js.DirectivePrologueStmt:
		w.literal(&js.LiteralExpr{TokenType: js.StringToken, Data: n.Value}, s, false)
		w.semi()
	case *js.VarDecl:
		w.take(n.TokenType)
		declaration := s
		if n.TokenType == js.VarToken {
			declaration = s.variable
		}
		for i, element := range n.List {
			if i > 0 {
				w.take(js.CommaToken)
			}
			w.bindingElement(element, s, declaration)
		}
		if !n.InFor && !n.InForInOf {
			w.semi()
		}
	case *js.FuncDecl:
		if n.Async {
			w.take(js.AsyncToken)
		}
		w.take(js.FunctionToken)
		if n.Generator {
			w.take(js.MulToken)
		}
		outer := s
		if n.Name != nil {
			declaration := s
			if w.expressions[n] {
				outer = w.scope(s, false)
				declaration = outer
			}
			w.identifier(n.Name.Data, outer, declaration, false, true)
		}
		bodyScope := w.parameters(n.Params, w.scope(outer, true), true)
		w.block(&n.Body, bodyScope)
		if !w.expressions[n] {
			w.semi()
		}
	case *js.ArrowFunc:
		if n.Async {
			w.take(js.AsyncToken)
		}
		bodyScope := w.parameters(n.Params, w.scope(s, true), w.tt == js.OpenParenToken)
		w.take(js.ArrowToken)
		if w.tt == js.OpenBraceToken {
			w.block(&n.Body, bodyScope)
		} else if len(n.Body.List) == 1 {
			if returned, ok := n.Body.List[0].(*js.ReturnStmt); ok {
				w.walk(returned.Value, bodyScope)
			} else {
				w.fail("arrow expression body")
			}
		} else {
			w.fail("arrow body")
		}
	case *js.MethodDecl:
		w.method(n, s)
	case *js.ClassDecl:
		w.take(js.ClassToken)
		classScope := w.scope(s, false)
		if n.Name != nil {
			declaration := s
			if w.expressions[n] {
				declaration = classScope
			}
			offset := w.start
			nameIndex := w.identifier(n.Name.Data, s, declaration, false, true)
			if nameIndex >= 0 {
				w.declare(w.tokens[nameIndex].token.Name, offset, classScope)
			}
		}
		if n.Extends != nil {
			w.take(js.ExtendsToken)
			w.walk(n.Extends, classScope)
		}
		w.take(js.OpenBraceToken)
		for _, element := range n.List {
			for w.err == nil && w.tt == js.SemicolonToken {
				w.take(js.SemicolonToken)
			}
			elementStart := w.start
			if element.StaticBlock != nil {
				w.take(js.StaticToken)
				w.block(element.StaticBlock, w.scope(classScope, true))
			} else if element.Method != nil {
				w.method(element.Method, classScope)
			} else {
				if element.Field.Static {
					w.take(js.StaticToken)
				}
				w.className(element.Field.Name, classScope)
				if element.Field.Init != nil {
					w.take(js.EqToken)
					w.walk(element.Field.Init, classScope)
				}
				w.semi()
			}
			w.span(elementStart)
		}
		for w.err == nil && w.tt == js.SemicolonToken {
			w.take(js.SemicolonToken)
		}
		w.take(js.CloseBraceToken)
		if !w.expressions[n] {
			w.semi()
		}
	case *js.IfStmt:
		w.take(js.IfToken)
		w.take(js.OpenParenToken)
		w.walk(n.Cond, s)
		w.take(js.CloseParenToken)
		w.walk(n.Body, s)
		if n.Else != nil {
			w.take(js.ElseToken)
			w.walk(n.Else, s)
		}
		w.semi()
	case *js.WhileStmt:
		w.take(js.WhileToken)
		w.take(js.OpenParenToken)
		w.walk(n.Cond, s)
		w.take(js.CloseParenToken)
		w.walk(n.Body, s)
		w.semi()
	case *js.DoWhileStmt:
		w.take(js.DoToken)
		w.walk(n.Body, s)
		w.take(js.WhileToken)
		w.take(js.OpenParenToken)
		w.walk(n.Cond, s)
		w.take(js.CloseParenToken)
		w.semi()
	case *js.ForStmt:
		loop := w.scope(s, false)
		w.take(js.ForToken)
		w.take(js.OpenParenToken)
		w.walk(n.Init, loop)
		w.take(js.SemicolonToken)
		w.walk(n.Cond, loop)
		w.take(js.SemicolonToken)
		w.walk(n.Post, loop)
		w.take(js.CloseParenToken)
		w.loopBody(n.Body, loop)
		w.semi()
	case *js.ForInStmt:
		loop := w.scope(s, false)
		w.take(js.ForToken)
		w.take(js.OpenParenToken)
		w.walk(n.Init, loop)
		w.take(js.InToken)
		w.walk(n.Value, loop)
		w.take(js.CloseParenToken)
		w.loopBody(n.Body, loop)
		w.semi()
	case *js.ForOfStmt:
		loop := w.scope(s, false)
		w.take(js.ForToken)
		if n.Await {
			w.take(js.AwaitToken)
		}
		w.take(js.OpenParenToken)
		w.walk(n.Init, loop)
		w.take(js.OfToken)
		w.walk(n.Value, loop)
		w.take(js.CloseParenToken)
		w.loopBody(n.Body, loop)
		w.semi()
	case *js.SwitchStmt:
		w.take(js.SwitchToken)
		w.take(js.OpenParenToken)
		w.walk(n.Init, s)
		w.take(js.CloseParenToken)
		w.take(js.OpenBraceToken)
		cases := w.scope(s, false)
		for _, clause := range n.List {
			clauseStart := w.start
			w.take(clause.TokenType)
			w.walk(clause.Cond, cases)
			w.take(js.ColonToken)
			for _, statement := range clause.List {
				w.walk(statement, cases)
			}
			w.span(clauseStart)
		}
		w.take(js.CloseBraceToken)
		w.semi()
	case *js.BranchStmt:
		w.take(n.Type)
		if n.Label != nil {
			w.identifier(n.Label, s, nil, true, false)
		}
		w.semi()
	case *js.LabelledStmt:
		w.identifier(n.Label, s, nil, true, false)
		w.take(js.ColonToken)
		w.walk(n.Value, s)
		w.semi()
	case *js.ReturnStmt:
		w.take(js.ReturnToken)
		w.walk(n.Value, s)
		w.semi()
	case *js.ThrowStmt:
		w.take(js.ThrowToken)
		w.walk(n.Value, s)
		w.semi()
	case *js.TryStmt:
		w.take(js.TryToken)
		w.block(n.Body, s)
		if n.Catch != nil {
			catchStart := w.start
			w.take(js.CatchToken)
			catch := w.scope(s, false)
			if n.Binding != nil {
				w.take(js.OpenParenToken)
				w.binding(n.Binding, catch, catch, false)
				w.take(js.CloseParenToken)
			}
			w.block(n.Catch, catch)
			w.span(catchStart)
		}
		if n.Finally != nil {
			w.take(js.FinallyToken)
			w.block(n.Finally, s)
		}
		w.semi()
	case *js.DebuggerStmt:
		w.take(js.DebuggerToken)
		w.semi()
	case *js.WithStmt:
		w.err = fmt.Errorf("Kiro JavaScript with 动态作用域无法验证词法绑定 at %d", start)
	case *js.GroupExpr:
		w.take(js.OpenParenToken)
		w.walk(n.X, s)
		w.take(js.CloseParenToken)
	case *js.IndexExpr:
		w.walk(n.X, s)
		if n.Optional {
			w.take(js.OptChainToken)
		}
		w.take(js.OpenBracketToken)
		w.walk(n.Y, s)
		w.take(js.CloseBracketToken)
	case *js.DotExpr:
		w.walk(n.X, s)
		if n.Optional {
			w.take(js.OptChainToken)
		} else {
			w.take(js.DotToken)
		}
		switch property := n.Y.(type) {
		case js.LiteralExpr:
			w.literal(&property, s, true)
		case *js.LiteralExpr:
			w.literal(property, s, true)
		case *js.Var:
			w.identifier(property.Data, s, nil, true, false)
		default:
			w.fail("dot property")
		}
	case *js.NewTargetExpr:
		w.identifier([]byte("new"), s, nil, true, false)
		w.take(js.DotToken)
		w.identifier([]byte("target"), s, nil, true, false)
	case *js.ImportMetaExpr:
		w.identifier([]byte("import"), s, nil, true, false)
		w.take(js.DotToken)
		w.identifier([]byte("meta"), s, nil, true, false)
	case *js.NewExpr:
		w.take(js.NewToken)
		w.walk(n.X, s)
		if n.Args != nil {
			w.arguments(n.Args, s)
		} else if w.tt == js.OpenParenToken {
			// 官方 AST 以 nil 表示空 constructor args；仍恢复源中存在的空括号。
			w.take(js.OpenParenToken)
			w.take(js.CloseParenToken)
		}
	case *js.CallExpr:
		w.walk(n.X, s)
		if n.Optional {
			w.take(js.OptChainToken)
		}
		w.arguments(&n.Args, s)
	case *js.UnaryExpr:
		op := n.Op
		switch op {
		case js.PosToken:
			op = js.AddToken
		case js.NegToken:
			op = js.SubToken
		case js.PreIncrToken, js.PostIncrToken:
			op = js.IncrToken
		case js.PreDecrToken, js.PostDecrToken:
			op = js.DecrToken
		}
		if n.Op == js.PostIncrToken || n.Op == js.PostDecrToken {
			w.walk(n.X, s)
			w.take(op)
		} else {
			w.take(op)
			w.walk(n.X, s)
		}
	case *js.BinaryExpr:
		w.walk(n.X, s)
		w.take(n.Op)
		w.walk(n.Y, s)
	case *js.CondExpr:
		w.walk(n.Cond, s)
		w.take(js.QuestionToken)
		w.walk(n.X, s)
		w.take(js.ColonToken)
		w.walk(n.Y, s)
	case *js.YieldExpr:
		w.take(js.YieldToken)
		if n.Generator {
			w.take(js.MulToken)
		}
		w.walk(n.X, s)
	case *js.CommaExpr:
		for i, expr := range n.List {
			if i > 0 {
				w.take(js.CommaToken)
			}
			w.walk(expr, s)
		}
	case *js.ArrayExpr:
		w.take(js.OpenBracketToken)
		for i, element := range n.List {
			if i > 0 {
				w.take(js.CommaToken)
			}
			elementStart := w.start
			if element.Spread {
				w.take(js.EllipsisToken)
			}
			w.walk(element.Value, s)
			if element.Value != nil {
				w.span(elementStart)
			}
		}
		if w.tt == js.CommaToken {
			w.take(js.CommaToken)
		}
		w.take(js.CloseBracketToken)
	case *js.ObjectExpr:
		w.take(js.OpenBraceToken)
		for i, property := range n.List {
			if i > 0 {
				w.take(js.CommaToken)
			}
			propertyStart := w.start
			if property.Spread {
				w.take(js.EllipsisToken)
				w.walk(property.Value, s)
			} else if method, ok := property.Value.(*js.MethodDecl); ok {
				w.method(method, s)
			} else {
				index := w.property(property.Name, s)
				if w.tt == js.ColonToken {
					w.take(js.ColonToken)
					w.walk(property.Value, s)
				} else if v, ok := property.Value.(*js.Var); ok && index >= 0 {
					t := &w.tokens[index]
					expected, _ := kiroStageJSDecodeIdentifier(v.Data)
					if t.token.Name != expected {
						w.fail("shorthand " + expected)
						return
					}
					t.resolve = true
					t.scope = s
				} else {
					w.fail("object property colon")
					return
				}
				if property.Init != nil {
					w.take(js.EqToken)
					w.walk(property.Init, s)
				}
			}
			w.span(propertyStart)
		}
		if w.tt == js.CommaToken {
			w.take(js.CommaToken)
		}
		w.take(js.CloseBraceToken)
	case *js.TemplateExpr:
		w.walk(n.Tag, s)
		if n.Optional {
			w.take(js.OptChainToken)
		}
		for _, part := range n.List {
			partStart := w.start
			w.template(part.Value, false)
			w.walk(part.Expr, s)
			w.span(partStart)
		}
		w.template(n.Tail, true)
	default:
		w.fail(fmt.Sprintf("supported AST node %T", n))
	}
}
