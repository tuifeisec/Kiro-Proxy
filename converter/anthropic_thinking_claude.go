package converter

import (
	"regexp"
	"strconv"
	"strings"
)

// claudeModelPattern 从各种中转写法中识别 Claude 系列与版本（思路参照 magpie anthropic.go claudeVersion/claudeLine）：
// claude-opus-4-6、claude-sonnet-4.6、anthropic.claude-sonnet-4-6-v1:0、claude-sonnet-4-5@20250929、
// 中转改名的 opus-5.5、claude-fable-5-1，以及旧顺序 claude-3-7-sonnet。
// 版本号最多两位并要求其后不是数字，避免把 -20250929 这类日期后缀当作次版本。
var claudeModelPattern = regexp.MustCompile(
	`(?:^|[^a-z0-9])(?:claude-)?(opus|sonnet|haiku|fable|mythos)-(\d{1,2})(?:[-.](\d{1,2}))?(?:[^0-9]|$)` +
		`|claude-(\d{1,2})(?:[-.](\d{1,2}))?-(opus|sonnet|haiku)`)

const (
	claudeFamilyOpus   = "opus"
	claudeFamilySonnet = "sonnet"
	claudeFamilyFable  = "fable"
	claudeFamilyMythos = "mythos"
)

// claudeVersion 是从模型 ID 解析出的 Claude 系列与版本；Family 为空表示无法识别为 Claude。
type claudeVersion struct {
	Family string
	Major  int
	Minor  int
}

func parseClaudeVersion(modelID string) claudeVersion {
	m := claudeModelPattern.FindStringSubmatch(strings.ToLower(modelID))
	if m == nil {
		return claudeVersion{}
	}
	family, major, minor := m[1], m[2], m[3]
	if family == "" {
		family, major, minor = m[6], m[4], m[5]
	}
	// 正则保证 major/minor 只含 1-2 位数字（minor 可缺省为 0），Atoi 不会失败
	majorNum, _ := strconv.Atoi(major)
	minorNum, _ := strconv.Atoi(minor)
	return claudeVersion{Family: family, Major: majorNum, Minor: minorNum}
}

func (v claudeVersion) known() bool { return v.Family != "" }

// adaptiveOnly：Claude 4.6 起只接受 thinking.type=adaptive，档位放在 output_config.effort
// （magpie adaptiveOnly 注释：claude-opus-5-5 对 enabled+budget 返回 "requires adaptive thinking"）。
func (v claudeVersion) adaptiveOnly() bool {
	return v.known() && (v.Major > 4 || v.Major == 4 && v.Minor >= 6)
}

// lacksXhigh：Opus/Sonnet 4.6 没有 xhigh 档，改发 max；之后的版本都有 xhigh（magpie noXhigh 注释）。
func (v claudeVersion) lacksXhigh() bool {
	return v.known() && v.Major == 4 && v.Minor == 6
}

// claudeThinkingOff 描述模型如何接受"关闭思考"。
type claudeThinkingOff int

const (
	claudeOffDisabled     claudeThinkingOff = iota // thinking.type=disabled
	claudeOffBetweenTools                          // 拒绝 disabled，要求 thinking.type=between_tools
	claudeOffNever                                 // 不能关闭思考：不写 thinking，档位降到最低
)

// thinkingOffRule 按 magpie thinkingOffOf 注释记录的 Anthropic 规则（需实测）：
//   - Sonnet 5.5 对 disabled 返回 400 "To turn thinking off on this model, send "thinking": {"type": "between_tools"}"；
//   - Opus 5.5、Fable 5、Mythos 5 在任何档位都拒绝 disabled（自适应思考常开）；
//   - Opus 5、Haiku 5.5 只在 xhigh/max 拒绝 disabled，none 档不涉及，按 disabled 发送；
//   - 4.x 及更早、Sonnet 5、Haiku 5 接受 disabled。
//
// 未知 Claude 版本与非 Claude 模型同样发 disabled。
func thinkingOffRule(v claudeVersion) claudeThinkingOff {
	switch {
	case v.Family == claudeFamilyFable || v.Family == claudeFamilyMythos:
		if v.Major == 5 {
			return claudeOffNever
		}
	case v.Major == 5 && v.Minor == 5:
		switch v.Family {
		case claudeFamilySonnet:
			return claudeOffBetweenTools
		case claudeFamilyOpus:
			return claudeOffNever
		}
	}
	return claudeOffDisabled
}
