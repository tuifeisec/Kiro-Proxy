package converter

import "testing"

func TestParseClaudeVersionSpellings(t *testing.T) {
	cases := map[string]claudeVersion{
		"claude-opus-4-6":                          {Family: "opus", Major: 4, Minor: 6},
		"claude-sonnet-4.6":                        {Family: "sonnet", Major: 4, Minor: 6},
		"anthropic.claude-sonnet-4-6-v1:0":         {Family: "sonnet", Major: 4, Minor: 6},
		"claude-sonnet-4-5@20250929":               {Family: "sonnet", Major: 4, Minor: 5},
		"claude-sonnet-4-5-20250929":               {Family: "sonnet", Major: 4, Minor: 5},
		"claude-opus-4-20250514":                   {Family: "opus", Major: 4},
		"claude-opus-4-1-20250805":                 {Family: "opus", Major: 4, Minor: 1},
		"opus-5.5":                                 {Family: "opus", Major: 5, Minor: 5},
		"Claude-Opus-5":                            {Family: "opus", Major: 5},
		"claude-fable-5-1":                         {Family: "fable", Major: 5, Minor: 1},
		"claude-mythos-5@20261101":                 {Family: "mythos", Major: 5},
		"claude-3-7-sonnet-20250219":               {Family: "sonnet", Major: 3, Minor: 7},
		"claude-3-opus-20240229":                   {Family: "opus", Major: 3},
		"claude-3-5-haiku-20241022":                {Family: "haiku", Major: 3, Minor: 5},
		"kiro/claude-sonnet-4.6[1m]":               {Family: "sonnet", Major: 4, Minor: 6},
		"claude-sonnet-latest":                     {},
		"deepseek-chat":                            {},
		"glm-5.2":                                  {},
		"gpt-6.1":                                  {},
		"anthropic.claude-haiku-5-5-20261001-v1:0": {Family: "haiku", Major: 5, Minor: 5},
	}
	for model, want := range cases {
		if got := parseClaudeVersion(model); got != want {
			t.Errorf("parseClaudeVersion(%q) = %+v, want %+v", model, got, want)
		}
	}
}
