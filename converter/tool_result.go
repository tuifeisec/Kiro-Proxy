package converter

import json "github.com/goccy/go-json"

type ToolResultConversionReport struct {
	Results       int
	Blocks        int
	NonTextBlocks int
}

// ToolResultConversionReportForAudit counts parsed tool-result blocks without
// copying their content into logs.
func ToolResultConversionReportForAudit(conv *ParsedConversation) ToolResultConversionReport {
	if conv == nil {
		return ToolResultConversionReport{}
	}
	report := ToolResultConversionReport{}
	visit := func(results []ParsedToolResult) {
		for _, result := range results {
			report.Results++
			report.Blocks += len(result.Blocks)
			if toolResultHasNonText(result) {
				for _, block := range result.Blocks {
					if block.Kind != "text" {
						report.NonTextBlocks++
					}
				}
			}
		}
	}
	visit(conv.CurrentToolResults)
	for _, entry := range conv.History {
		visit(entry.ToolResults)
	}
	return report
}

// serializeToolResultForProvider keeps the existing string representation for
// text-only results. Mixed or unknown blocks use a deterministic JSON envelope
// so string-only provider fields still retain every original block.
func serializeToolResultForProvider(result ParsedToolResult) string {
	if !toolResultHasNonText(result) {
		return result.Content
	}

	blocks := make([]json.RawMessage, 0, len(result.Blocks))
	for _, block := range result.Blocks {
		raw := block.Raw
		if block.Kind == "text" || len(raw) == 0 {
			raw, _ = json.Marshal(struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}{Type: "text", Text: block.Text})
		}
		if canonical := canonicalToolJSON(raw); len(canonical) > 0 {
			blocks = append(blocks, json.RawMessage(canonical))
		}
	}

	if len(blocks) == 0 {
		return result.Content
	}

	envelope := struct {
		Blocks []json.RawMessage `json:"_kiro_content_blocks"`
		Text   string            `json:"_kiro_text,omitempty"`
	}{
		Blocks: blocks,
		Text:   result.Content,
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return result.Content
	}
	return string(encoded)
}

func toolResultHasNonText(result ParsedToolResult) bool {
	for _, block := range result.Blocks {
		if block.Kind != "text" {
			return true
		}
	}
	return false
}
