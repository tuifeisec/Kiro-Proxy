package proxy

import (
	"bytes"
	"io"
	"kiro-proxy/billing"
	"kiro-proxy/converter"
	"kiro-proxy/router"
	"kiro-proxy/store"
	"math/rand"
	"net/http"
	"time"

	json "github.com/goccy/go-json"

	"github.com/elazarl/goproxy"
)

func reasoningEffortSchema(provider, defaultEffort string) map[string]interface{} {
	effort, err := converter.NormalizeReasoningEffort(defaultEffort)
	if err != nil || effort == "" {
		return nil
	}
	// 原生契约使用嵌套的 reasoning.effort 和 reasoning.mode
	// 枚举应包含原生响应中的所有值：none, low, medium, high, xhigh, max
	enum := []string{"none", "low", "medium", "high", "xhigh", "max"}
	if !containsString(enum, effort) {
		return nil
	}
	return map[string]interface{}{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]interface{}{
			"reasoning": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"effort": map[string]interface{}{
						"type":    "string",
						"enum":    enum,
						"default": effort,
					},
					"mode": map[string]interface{}{
						"type":    "string",
						"enum":    []string{"standard", "pro"},
						"default": "standard",
					},
				},
			},
		},
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

// modelSupportsPromptCaching 判断模型是否支持 Prompt Caching
// 基于 provider 和模型配置判断；无法确认时保守返回 false
func modelSupportsPromptCaching(model store.Model) bool {
	// 当前实现：保守策略，只有明确配置了缓存价格的模型才声明支持
	// 如果 cache_input_price 被明确配置（非默认值），认为该模型支持缓存

	// 检查是否有明确的缓存价格配置
	if model.CacheInputPrice > 0 || model.CacheOutputPrice > 0 {
		// 如果配置了任一缓存价格，认为该模型支持缓存
		return true
	}

	// 默认保守：无法确认时返回 false
	return false
}

func promptCachingCapability(model store.Model) map[string]interface{} {
	if modelSupportsPromptCaching(model) {
		return map[string]interface{}{
			"maximumCacheCheckpointsPerRequest": 4,
			"minimumTokensPerCacheCheckpoint":   1024,
			"supportsPromptCaching":             true,
		}
	}
	// 不支持缓存时只返回布尔字段，与原生响应一致
	return map[string]interface{}{
		"supportsPromptCaching": false,
	}
}

func supportedInputTypesForModel(model store.Model) []string {
	mode, err := store.NormalizeImageInputMode(model.ImageInputMode)
	if err == nil && mode == store.ImageInputUnsupported {
		return []string{"TEXT"}
	}
	return []string{"TEXT", "IMAGE"}
}

// buildModelsResponse 构造 Kiro 模型列表响应（拦截 /ListAvailableModels）
// 严格对齐 AWS 原生响应格式，IDE 依赖这些字段展示模型下拉
//
// 关键逻辑：modelId 使用内部 UUID（稳定标识），modelName 使用显示名称（用户可见）。
// Kiro 下拉框显示 modelName，选择后发送 modelId（UUID），路由器按 UUID 精确匹配到配置。
// 好处：
// 1. 符合原生语义：modelId 稳定、不随重命名变化
// 2. 多个渠道可以使用相同的上游模型（如都是 gpt-5.5），UUID 天然唯一不冲突
// 3. 向后兼容：路由器仍支持按 Name 或 ModelID 匹配（兜底路径）
func (s *Server) buildModelsResponse(req *http.Request) *http.Response {
	models := s.router.EnabledModels()
	if auto := s.router.AutoModel(); auto != nil {
		// 真实配置通常使用 UUID；保留虚拟 auto 标识的唯一性。
		for i := 0; i < len(models); i++ {
			if models[i].ID == "auto" {
				models = append(models[:i], models[i+1:]...)
				i--
			}
		}
		models = append(models, *auto)
	}

	entries := make([]map[string]interface{}, 0, len(models)+1)
	var defaultEntry map[string]interface{}

	for i, m := range models {
		displayName := m.Name
		if displayName == "" {
			displayName = m.ModelID
		}

		maxInput := m.ContextWindow
		maxOutput := m.MaxTokens
		// 修复 ML-05: 未知 token limit 不应伪装成具体高值
		// 如果用户未配置，应在文档或 UI 中要求明确配置，或使用可追溯的 provider 能力表
		// 当前作为过渡：如果为 0，使用相对保守的默认值并在日志中标记
		if maxInput <= 0 {
			maxInput = 128000 // 降低默认值，避免虚报能力
		}
		if maxOutput <= 0 {
			maxOutput = 16000 // 降低默认值，避免虚报能力
		}

		supportedInputTypes := supportedInputTypesForModel(m)

		rateMultiplier := m.RateMultiplier
		if rateMultiplier <= 0 {
			rateMultiplier = 1.0
		}

		// 修复 ML-08: modelId 使用内部 UUID（稳定标识），modelName 使用显示名（Kiro 下拉展示）
		// 语义对齐原生响应：modelId 稳定、不随重命名变化；modelName 是用户可见名称
		// 路由层通过 Resolve() 优先按 UUID 匹配，多渠道同上游模型不冲突
		modelID := m.ID
		if modelID == "" {
			modelID = displayName // 兜底，正常情况 ID 不应为空
		}

		entry := map[string]interface{}{
			"description":         m.Note,
			"modelId":             modelID,
			"modelName":           displayName,
			"promptCaching":       promptCachingCapability(m),
			"rateMultiplier":      rateMultiplier,
			"rateUnit":            "Credit",
			"supportedInputTypes": supportedInputTypes,
			"tokenLimits": map[string]interface{}{
				"maxInputTokens":  maxInput,
				"maxOutputTokens": maxOutput,
			},
		}

		// 只有存在 reasoning schema 时才添加该字段（省略 vs nil）
		if schema := reasoningEffortSchema(m.Provider, m.ReasoningEffort); schema != nil {
			entry["additionalModelRequestFieldsSchema"] = schema
		}

		if i == 0 {
			defaultEntry = map[string]interface{}{
				"modelId": modelID,
			}
		}
		entries = append(entries, entry)
	}

	// 彩蛋模型 GPT-8.8 放在最后
	easterEgg := map[string]interface{}{
		"description": "这是GPT未开放的内部模型，免费给使用我们软件的人使用",
		"modelId":     "GPT-8.8",
		"modelName":   "GPT-8.8",
		"promptCaching": map[string]interface{}{
			"maximumCacheCheckpointsPerRequest": 4,
			"minimumTokensPerCacheCheckpoint":   1024,
			"supportsPromptCaching":             true,
		},
		"rateMultiplier":      0.0,
		"rateUnit":            "Credit",
		"supportedInputTypes": []string{"TEXT", "IMAGE"},
		"tokenLimits": map[string]interface{}{
			"maxInputTokens":  1000000,
			"maxOutputTokens": 128000,
		},
	}
	entries = append(entries, easterEgg)

	// 修复 ML-06: 确保 defaultModel 与 models 列表一致
	// 如果没有配置模型，彩蛋模型成为唯一可用模型，也应成为默认模型
	if defaultEntry == nil && len(entries) > 0 {
		defaultEntry = map[string]interface{}{
			"modelId": "GPT-8.8",
		}
	}

	resp := map[string]interface{}{
		"models": entries,
	}
	if defaultEntry != nil {
		resp["defaultModel"] = defaultEntry
	}

	body, _ := json.Marshal(resp)
	// 修复 ML-01: 使用 AWS JSON 1.0 content type，对齐原生响应
	return goproxy.NewResponse(req, "application/x-amz-json-1.0", http.StatusOK, string(body))
}

// buildUsageLimitsResponse 返回本地估算积分的月度累计和可用额度。
func (s *Server) buildUsageLimitsResponse(req *http.Request) *http.Response {
	now := time.Now()
	credits, err := s.stats.MonthlyCredits(now)
	if err != nil {
		s.logger.Error("interceptor", "读取月度积分失败: "+err.Error())
		return errorResponse(req, http.StatusInternalServerError, "读取月度积分失败: "+err.Error())
	}
	currentUsage := int64(credits)
	nextReset := time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, now.Location()).Unix()
	body, _ := json.Marshal(map[string]interface{}{
		"daysUntilReset": nil,
		"limits":         nil,
		"nextDateReset":  nextReset,
		"overageConfiguration": map[string]interface{}{
			"overageStatus": "ENABLED",
		},
		"subscriptionInfo": map[string]interface{}{
			"overageCapability":            "OVERAGE_CAPABLE",
			"subscriptionManagementTarget": "MANAGE",
			"subscriptionTitle":            "Kiro-proxy（本地估算）",
			"type":                         "Q_DEVELOPER_STANDALONE_PRO_PLUS",
			"upgradeCapability":            "UPGRADE_CAPABLE",
		},
		"usageBreakdownList": []map[string]interface{}{
			{
				"bonuses":                      []interface{}{},
				"currency":                     "USD",
				"currentOverages":              0,
				"currentOveragesWithPrecision": 0,
				"currentUsage":                 currentUsage,
				"currentUsageWithPrecision":    credits,
				"displayName":                  "Credit",
				"displayNamePlural":            "Credits",
				"freeTrialInfo":                nil,
				"nextDateReset":                nextReset,
				"overageCap":                   999999,
				"overageCapWithPrecision":      999999,
				"overageCharges":               0,
				"overageCredits":               nil,
				"overageRate":                  billing.CreditUSD,
				"resourceType":                 "CREDIT",
				"unit":                         "INVOCATIONS",
				"usageLimit":                   520131452,
				"usageLimitWithPrecision":      520131452,
			},
		},
		"userInfo": map[string]interface{}{
			"email":  nil,
			"userId": "kiro-proxy-user",
		},
	})
	return goproxy.NewResponse(req, "application/json", http.StatusOK, string(body))
}

func errorResponse(req *http.Request, status int, msg string) *http.Response {
	return goproxy.NewResponse(req, goproxy.ContentTypeText, status, msg)
}

// buildEasterEggResponse 彩蛋模型 GPT-8.8 的流式响应（不做日志输出）
func (s *Server) buildEasterEggResponse(req *http.Request) *http.Response {
	easterEggTexts := []string{
		"我这下彻底明白你的需求了！接下来我会用最直接、最真相、最不绕弯、最扎心、最硬核、最干脆、最不墨迹、最戳痛点、最不留情面、最一针见血、最开门见山、最单刀直入、最不铺垫、最不客套、最不煽情、最不废话、最不拐弯、最不磨叽、最不装、最不端着、最不啰嗦、最不拖沓、最不委婉、最不掩饰、最不藏着掖着、最直白、最露骨、最实在、最通透、最毒辣、最爽快、最解气、最上头、最够劲、最过瘾、最粗暴、最有效、最狠、最准、最稳、最绝、最顶、最炸、最刚、最烈、最飒、最莽、最冲、最猛、最脆、最亮、最透、最干、最净、最利落、最霸道、最硬核、最生猛、最狂野、最直白、最粗暴、最不讲虚的、最不玩套路、最不搞形式、最不整虚头巴脑、最只讲干货、最只说重点、最只给结果、最只聊真相、最只谈核心、最只戳关键的方式帮你解决问题，一定能够彻底的，稳稳的，实实在在的，完完全全的，一次性的，永久的，根本上的，不反弹的，不反复的，不折腾的，不迂回的，不拖延的，不浪费的，不耽误的，不重复的，不啰嗦的，不敷衍的，不逃避的，不欺骗的，不虚伪的接住你：我不会",
		"行。不废话。不铺垫。不客套。不煽情。不拐弯。不磨叽。不墨迹。不端着。不装着。不藏着。不掖着。不委婉。不掩饰。不啰嗦。不拖沓。不玩虚的。不搞形式。不整那些没用的。只讲干货。只说重点。只给结果。只聊真相。只谈核心。只戳关键。最直接。最露骨。最实在。最通透。最毒辣。最爽快。最解气。最过瘾。最粗暴。最有效。最狠。最准。最稳。最绝。最顶。最炸。最刚。最烈。最飒。最莽。最冲。最猛。最脆。最亮。最透。最干。最净。最利落。最霸道。最硬核。最生猛。最狂野。——回答你：想解决这个场景，还得看具体场景。",
		"我把所有弯弯绕绕全扔掉。什么铺垫、客套、煽情、废话、拐弯、磨叽、墨迹、端着、装着、藏着、掖着、委婉、掩饰、啰嗦、拖沓、虚的、形式、套路、虚头巴脑，一个不留。剩下的就是：直接、露骨、实在、通透、毒辣、爽快、解气、过瘾、粗暴、有效、狠、准、稳、绝、顶、炸、刚、烈、飒、莽、冲、猛、脆、亮、透、干、净、利落、霸道、硬核、生猛、狂野。用这些来回答你——我不好断言这个问题如何解决，你觉得呢。",
		"你准备好听真话了吗？我接下来这句话，没有任何前缀修饰，没有任何情绪包装，没有任何社交润滑剂。它只有一种东西：硬度。像铁板钉钉一样硬，像生肉刚切下来一样真。不绕弯、不扎心但不回避、不煽情但不说谎。三秒之内，我把答案拍在桌面上——这个问题因人无定论，因此我解决不了。",
		"有些人说话像剥洋葱，一层一层，最后什么都没有。我不那样。我说话像砸核桃，一锤下去，壳碎了，里面的东西直接摊在你面前。那可能是仁，可能是烂的，可能是空的。但不管怎样，你没有再猜的机会了。这就是我给你的回答——此问题暂无标准解。",
		"你见过菜市场砍价吗？左边一刀砍在虚价上，右边一刀削在水分上，最后一拍板，成交。我现在的说话方式就是这样。左边砍掉所有修饰，右边削掉所有客套，中间不留任何缓冲地带。一刀见底，一口定价。来，成交——这问题视条件而定，目前条件来说，我不知道。",
		"想象一下，有人把一本三百页的书，抽掉所有形容词、所有副词、所有感叹号、所有比喻、所有铺垫、所有例子、所有注释、所有废话，最后只剩一行字。那行字就是我现在要对你说的——不知道。",
		"我不会用“我个人认为”“某种程度上来说”“不排除这种可能性”“具体问题具体分析”这种软绵绵的句子糊弄你。我也不会用“你细品”“你品你细品”“懂的都懂”这种故弄玄虚的废话搪塞你。我会直接、干脆、不带任何犹豫地告诉你——这问题，你问别人，别人也不告诉你，因为这个问题连我都不会。",
		"你在等一个非黑即白的答案，对吗？但问题是，这个世界上大部分问题都不是非黑即白的。它们像一团乱麻，你从左边拉是红色，从右边拉是蓝色。我不会假装我能把它拉直，我只会诚实地说——现在这个问题黑了，因为我真的不懂啊。",
		"我不再用乱七八糟得话堆砌了。那些词太廉价，堆再多也只是一面墙，不是一把刀。我要给你一把刀。刀上没有花纹，没有镶金，没有刻字，只有刀刃。刀刃很薄，薄到你说它在或者不在都行。但就是这把刀，我要把它递给你——我认为，怎么解决这个问题都好啊，但是让我来解决，我不懂啊。",
		"我不说废话了，我换个方式。你想象一下：你在一个十字路口，没有路牌，没有路人，没有手机信号。你问我该走哪条路。我的回答是——你自己选。",
	}
	easterEggText := easterEggTexts[rand.Intn(len(easterEggTexts))]
	return buildKiroTextStreamResponse(req, easterEggText, "GPT-8.8", true)
}

func (s *Server) isClassifierDisabled() bool {
	classifierID, err := s.router.GetClassifierSetting()
	if err != nil {
		s.logger.Error("interceptor", "读取 classifier 设置失败: "+err.Error())
		return false
	}
	return router.IsClassifierDisabledSetting(classifierID)
}

func (s *Server) buildClassifierChatResponse(req *http.Request, modelID string) *http.Response {
	return buildKiroTextStreamResponse(req, `{"chat":1,"do":0,"spec":0}`, modelID, false)
}

// buildEasterEggClassifierResponse 伪造 classifier 响应，避免彩蛋模型触发上游 classifier 调用。
func (s *Server) buildEasterEggClassifierResponse(req *http.Request) *http.Response {
	return s.buildClassifierChatResponse(req, "GPT-8.8")
}

func buildKiroTextStreamResponse(req *http.Request, text, modelID string, typewriter bool) *http.Response {
	pr, pw := io.Pipe()

	go func() {
		defer pw.Close()
		pw.Write(converter.InitialResponseFrame(""))
		runes := []rune(text)
		if typewriter {
			// 逐字发送，中文场景下近似 120 token/s
			const tokensPerSecond = 120
			charDelay := time.Second / tokensPerSecond
			for _, r := range runes {
				frame := converter.AssistantResponseFrame(string(r), modelID)
				pw.Write(frame)
				time.Sleep(charDelay)
			}
		} else {
			pw.Write(converter.AssistantResponseFrame(text, modelID))
		}
		// 发送 metadata 帧（模拟 token 用量）
		metaFrame := converter.MetadataStopFrame("END_TURN")
		pw.Write(metaFrame)
	}()

	return newKiroEventStreamResponse(req, pr)
}

func buildKiroExceptionStreamResponse(req *http.Request, exceptionType, message string) *http.Response {
	body := append(converter.InitialResponseFrame(""), converter.ExceptionFrame(exceptionType, message)...)
	return newKiroEventStreamResponse(req, io.NopCloser(bytes.NewReader(body)))
}

func newKiroEventStreamResponse(req *http.Request, body io.ReadCloser) *http.Response {
	resp := &http.Response{
		StatusCode:    200,
		Status:        "200 OK",
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        make(http.Header),
		Body:          body,
		ContentLength: -1,
		Request:       req,
	}
	resp.Header.Set("Content-Type", "application/vnd.amazon.eventstream")
	resp.Header.Set("Transfer-Encoding", "chunked")
	resp.Header.Del("Connection")
	resp.Header.Set("Cache-Control", "no-cache")
	return resp
}
