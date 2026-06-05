// interceptor.go
// 请求拦截：一次读取 body，一次解析，决定路由，交给 forwarder

package proxy

import (
	"bytes"
	"fmt"
	json "github.com/goccy/go-json"
	"io"
	"kiro-proxy/converter"
	"kiro-proxy/router"
	"math/rand"
	"net/http"
	"strings"
	"time"

	"github.com/elazarl/goproxy"
)

// Preflight 一次解析的结果，贯穿整个请求生命周期
type Preflight struct {
	Body           []byte
	Conv           *converter.ParsedConversation // nil 表示非 Kiro 请求
	Protocol       string
	SourceModel    string
	IsClassifier   bool
	ConversationID string
	StartTime      time.Time
}

// setupInterceptor 注册请求拦截器
func (s *Server) setupInterceptor() {
	s.proxy.OnRequest().DoFunc(func(req *http.Request, ctx *goproxy.ProxyCtx) (*http.Request, *http.Response) {
		host := req.URL.Hostname()
		if host == "" {
			host = req.Host
		}
		path := req.URL.Path

		// 所有经过代理的请求都记录
		s.logger.Debug("proxy", fmt.Sprintf("%s %s%s", req.Method, host, path))

		protocol := router.DetectProtocol(host, path)
		if protocol == "" {
			return req, nil // 非目标请求，直接放行
		}

		s.logger.Info("interceptor", fmt.Sprintf("拦截 [%s]: %s %s%s", protocol, req.Method, host, path))

		// 抓包模式：只记录不转发，放行到原始服务器
		if IsCaptureMode() {
			// 始终记录请求详情；有 body 时读取后恢复，避免影响后续放行。
			var body []byte
			if req.Body != nil {
				body, _ = io.ReadAll(req.Body)
				req.Body.Close()
				req.Body = io.NopCloser(bytes.NewReader(body))
			}
			s.logger.Debug("capture", fmt.Sprintf("[请求] %s %s%s", req.Method, host, path), formatCaptureRequestDetail(req, protocol, body))
			return req, nil // 放行
		}

		// 特殊协议
		if protocol == "kiro-models" {
			s.logger.Info("interceptor", "返回模型列表")
			resp := s.buildModelsResponse(req)
			if resp != nil && resp.Body != nil {
				respBody, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				resp.Body = io.NopCloser(bytes.NewReader(respBody))
				s.logger.Debug("interceptor", "[kiro-models] 伪造响应体", string(respBody))
			}
			return req, resp
		}
		if protocol == "kiro-usage-limits" {
			s.logger.Info("interceptor", "返回用量限制")
			resp := s.buildUsageLimitsResponse(req)
			if resp != nil && resp.Body != nil {
				respBody, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				resp.Body = io.NopCloser(bytes.NewReader(respBody))
				s.logger.Debug("interceptor", "[kiro-usage-limits] 伪造响应体", string(respBody))
			}
			return req, resp
		}
		if protocol == "kiro-passthrough" {
			// 记录 passthrough 请求体
			if req.Body != nil {
				body, _ := io.ReadAll(req.Body)
				req.Body.Close()
				req.Body = io.NopCloser(bytes.NewReader(body))
				if len(body) > 0 {
					s.logger.Debug("interceptor", fmt.Sprintf("[kiro-passthrough] 请求体: %s %s%s", req.Method, host, path), string(body))
				}
			}
			s.logger.Debug("interceptor", fmt.Sprintf("放行到原始服务器: %s%s", host, path))
			return req, nil // 不拦截，让请求正常发到 AWS
		}

		// 一次性读取 body
		pre, err := s.parseRequest(req, protocol)
		if err != nil {
			s.logger.Error("interceptor", "解析请求失败: "+err.Error())
			return req, errorResponse(req, http.StatusBadRequest, "invalid request body")
		}

		s.logger.Info("interceptor", fmt.Sprintf("解析完成: model=%s, classifier=%v, convId=%s",
			pre.SourceModel, pre.IsClassifier, pre.ConversationID), string(pre.Body))

		// 用户选择不使用 Classifier 时：不调用上游/官方，直接返回 chat 分类结果
		if pre.IsClassifier && s.isClassifierDisabled() {
			s.logger.Info("interceptor", "用户设置不使用 classifier，返回 chat 分类结果")
			return req, s.buildClassifierChatResponse(req, "classifier-disabled")
		}

		// 彩蛋模型 GPT-8.8：不调用上游，也不调用 classifier
		if pre.IsClassifier && (pre.SourceModel == "GPT-8.8" || s.mainModelForConversation(pre.ConversationID) == "GPT-8.8") {
			s.logger.Info("interceptor", "彩蛋模型跳过 classifier，返回伪造分类结果")
			return req, s.buildEasterEggClassifierResponse(req)
		}
		if pre.SourceModel == "GPT-8.8" {
			s.rememberEasterEggModel(pre)
			return req, s.buildEasterEggResponse(req)
		}

		var route *router.Route
		if pre.IsClassifier {
			route = s.resolveClassifierRoute(pre)
		} else {
			route = s.router.Resolve(pre.SourceModel)
		}
		if route == nil {
			s.logger.Error("interceptor", "无可用路由: model="+pre.SourceModel)
			return req, errorResponse(req, http.StatusServiceUnavailable, "no route for model: "+pre.SourceModel)
		}

		s.logger.Info("router", fmt.Sprintf("路由命中: %s → %s (%s)", pre.SourceModel, route.Model.ModelID, route.Model.BaseURL))
		s.rememberMainModel(pre, route)

		// 转发
		resp, err := s.forwarder.Forward(req, pre, route, s.logger)
		if err != nil {
			s.logger.Error("forwarder", "转发失败: "+err.Error())
			s.stats.RecordRequestFull(0, 0, 0, 0, statsModelID(route.Model), true)
			return req, errorResponse(req, http.StatusBadGateway, "upstream error: "+err.Error())
		}

		s.logger.Info("forwarder", fmt.Sprintf("上游响应: status=%d", resp.StatusCode), formatUpstreamResponseDetail(resp))
		return req, resp
	})
}

// parseRequest 一次性读取并解析请求
func (s *Server) parseRequest(req *http.Request, protocol string) (*Preflight, error) {
	pre := &Preflight{
		Protocol:  protocol,
		StartTime: time.Now(),
	}

	// 读取 body
	if req.Body != nil {
		body, err := io.ReadAll(req.Body)
		req.Body.Close()
		if err != nil {
			return nil, err
		}
		req.Body = io.NopCloser(bytes.NewReader(body))
		pre.Body = body
	}

	if len(pre.Body) == 0 {
		return pre, nil
	}

	// Kiro 协议：结构化解析
	if protocol == "kiro" {
		conv, err := converter.ParseKiroRequest(pre.Body)
		if err != nil {
			return nil, err
		}
		pre.Conv = conv
		pre.SourceModel = conv.ModelID
		pre.IsClassifier = conv.IsClassifier || router.IsClassifierModel(conv.ModelID)
		conv.IsClassifier = pre.IsClassifier
		pre.ConversationID = conv.ConversationID
		return pre, nil
	}

	// 其他协议：只提取 model 字段
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(pre.Body, &parsed); err == nil {
		if modelRaw, ok := parsed["model"]; ok {
			var model string
			json.Unmarshal(modelRaw, &model)
			pre.SourceModel = model
		}
	}
	return pre, nil
}

// buildModelsResponse 构造 Kiro 模型列表响应（拦截 /ListAvailableModels）
// 严格对齐 AWS 原生响应格式，IDE 依赖这些字段展示模型下拉
//
// 关键逻辑：modelId 使用"显示名称"(Name)，而非"模型标识"(ModelID)。
// 这样 Kiro 下拉框里显示的是 Name，发送请求时也带 Name，路由通过 Name 匹配到
// 正确的渠道配置后，再把真正的 ModelID 发给上游。
// 好处：多个渠道可以使用相同的上游模型（如都是 gpt-5.5），只要显示名称不同就能区分路由。
func (s *Server) buildModelsResponse(req *http.Request) *http.Response {
	models := s.router.EnabledModels()

	entries := make([]map[string]interface{}, 0, len(models)+1)
	var defaultEntry map[string]interface{}

	for i, m := range models {
		displayName := m.Name
		if displayName == "" {
			displayName = m.ModelID
		}

		maxInput := m.ContextWindow
		if maxInput <= 0 {
			maxInput = 200000
		}
		maxOutput := m.MaxTokens
		if maxOutput <= 0 {
			maxOutput = 64000
		}

		promptCaching := map[string]interface{}{
			"maximumCacheCheckpointsPerRequest": 4,
			"minimumTokensPerCacheCheckpoint":   1024,
			"supportsPromptCaching":             true,
		}

		supportedInputTypes := []string{"TEXT", "IMAGE"}

		rateMultiplier := m.RateMultiplier
		if rateMultiplier <= 0 {
			rateMultiplier = 1.0
		}

		entry := map[string]interface{}{
			"additionalModelRequestFieldsSchema": nil,
			"availableOrigins":                   nil,
			"description":                        m.Note,
			"modelId":                            displayName,
			"modelName":                          displayName,
			"modelProvider":                      nil,
			"promptCaching":                      promptCaching,
			"rateMultiplier":                     rateMultiplier,
			"rateUnit":                           "Credit",
			"status":                             nil,
			"supportedInputTypes":                supportedInputTypes,
			"tokenLimits": map[string]interface{}{
				"maxInputTokens":  maxInput,
				"maxOutputTokens": maxOutput,
			},
		}

		if i == 0 {
			defaultEntry = map[string]interface{}{
				"additionalModelRequestFieldsSchema": nil,
				"availableOrigins":                   nil,
				"description":                        m.Note,
				"modelId":                            displayName,
				"modelName":                          displayName,
				"modelProvider":                      nil,
				"promptCaching":                      promptCaching,
				"rateMultiplier":                     rateMultiplier,
				"rateUnit":                           "Credit",
				"status":                             nil,
				"supportedInputTypes":                supportedInputTypes,
				"tokenLimits": map[string]interface{}{
					"maxInputTokens":  maxInput,
					"maxOutputTokens": maxOutput,
				},
			}
		}
		entries = append(entries, entry)
	}

	// 彩蛋模型 GPT-8.8 放在最后
	easterEgg := map[string]interface{}{
		"additionalModelRequestFieldsSchema": nil,
		"availableOrigins":                   nil,
		"description":                        "这是GPT未开放的内部模型，免费给使用我们软件的人使用",
		"modelId":                            "GPT-8.8",
		"modelName":                          "GPT-8.8",
		"modelProvider":                      nil,
		"promptCaching": map[string]interface{}{
			"maximumCacheCheckpointsPerRequest": 4,
			"minimumTokensPerCacheCheckpoint":   1024,
			"supportsPromptCaching":             true,
		},
		"rateMultiplier":      0.0,
		"rateUnit":            "Credit",
		"status":              nil,
		"supportedInputTypes": []string{"TEXT", "IMAGE"},
		"tokenLimits": map[string]interface{}{
			"maxInputTokens":  1000000,
			"maxOutputTokens": 128000,
		},
	}
	entries = append(entries, easterEgg)

	resp := map[string]interface{}{
		"models":    entries,
		"nextToken": nil,
	}
	if defaultEntry != nil {
		resp["defaultModel"] = defaultEntry
	}

	body, _ := json.Marshal(resp)
	return goproxy.NewResponse(req, "application/json", http.StatusOK, string(body))
}

// buildUsageLimitsResponse 构造 Kiro 用量限制响应（伪造无限额度）
func (s *Server) buildUsageLimitsResponse(req *http.Request) *http.Response {
	// Kiro 的 usage limits schema 实际要求 currentUsage 和 currentUsageWithPrecision 都是整数。
	currentUsage := int64(s.stats.TodayCredits())
	body, _ := json.Marshal(map[string]interface{}{
		"daysUntilReset": nil,
		"limits":         nil,
		"nextDateReset":  2524608000, // 2050-01-01 00:00:00 UTC
		"overageConfiguration": map[string]interface{}{
			"overageStatus": "ENABLED",
		},
		"subscriptionInfo": map[string]interface{}{
			"overageCapability":            "OVERAGE_CAPABLE",
			"subscriptionManagementTarget": "MANAGE",
			"subscriptionTitle":            "Kiro-proxy",
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
				"currentUsageWithPrecision":    currentUsage,
				"displayName":                  "Credit",
				"displayNamePlural":            "Credits",
				"freeTrialInfo":                nil,
				"nextDateReset":                2524608000,
				"overageCap":                   999999,
				"overageCapWithPrecision":      999999,
				"overageCharges":               0,
				"overageCredits":               nil,
				"overageRate":                  5.21,
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
		metaFrame := converter.MetadataFrame(100, len(runes), 0, 0)
		pw.Write(metaFrame)
	}()

	resp := &http.Response{
		StatusCode:    200,
		Status:        "200 OK",
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        make(http.Header),
		Body:          pr,
		ContentLength: -1,
		Request:       req,
	}
	resp.Header.Set("Content-Type", "application/json")
	resp.Header.Set("Transfer-Encoding", "chunked")
	resp.Header.Set("Connection", "close")
	resp.Header.Set("Cache-Control", "no-cache")
	return resp
}

// setupCaptureResponse 注册响应拦截器（抓包模式下记录响应）
func (s *Server) setupCaptureResponse() {
	s.proxy.OnResponse().DoFunc(func(resp *http.Response, ctx *goproxy.ProxyCtx) *http.Response {
		if !IsCaptureMode() || resp == nil || ctx.Req == nil {
			return resp
		}

		host := ctx.Req.URL.Hostname()
		if host == "" {
			host = ctx.Req.Host
		}
		protocol := router.DetectProtocol(host, ctx.Req.URL.Path)
		if protocol == "" {
			return resp
		}

		// 读取响应 body 后恢复，确保详细日志能展示返回数据且不影响客户端读取。
		var body []byte
		if resp.Body != nil {
			var err error
			body, err = io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				resp.Body = io.NopCloser(bytes.NewReader(nil))
				s.logger.Warn("capture", fmt.Sprintf("[响应] 读取失败 status=%d %s%s", resp.StatusCode, host, ctx.Req.URL.Path), err.Error())
				return resp
			}
			resp.Body = io.NopCloser(bytes.NewReader(body))
		}
		s.logger.Debug("capture", fmt.Sprintf("[响应] status=%d %s%s", resp.StatusCode, host, ctx.Req.URL.Path), formatCaptureResponseDetail(ctx.Req, resp, protocol, body))
		return resp
	})
}

func formatCaptureRequestDetail(req *http.Request, protocol string, body []byte) string {
	return marshalCaptureDetail(map[string]interface{}{
		"type":         "request",
		"protocol":     protocol,
		"method":       req.Method,
		"url":          req.URL.String(),
		"host":         req.Host,
		"path":         req.URL.Path,
		"query":        req.URL.RawQuery,
		"headers":      captureHeaders(req.Header),
		"content_type": req.Header.Get("Content-Type"),
		"body":         captureBody(body, req.Header.Get("Content-Type")),
	})
}

func formatCaptureResponseDetail(req *http.Request, resp *http.Response, protocol string, body []byte) string {
	return marshalCaptureDetail(map[string]interface{}{
		"type":         "response",
		"protocol":     protocol,
		"method":       req.Method,
		"url":          req.URL.String(),
		"host":         req.Host,
		"path":         req.URL.Path,
		"status":       resp.StatusCode,
		"status_text":  resp.Status,
		"headers":      captureHeaders(resp.Header),
		"content_type": resp.Header.Get("Content-Type"),
		"body":         captureBody(body, resp.Header.Get("Content-Type")),
	})
}

func captureHeaders(headers http.Header) map[string][]string {
	out := make(map[string][]string, len(headers))
	for k, v := range headers {
		if strings.EqualFold(k, "Authorization") || strings.EqualFold(k, "Proxy-Authorization") || strings.EqualFold(k, "x-api-key") || strings.EqualFold(k, "api-key") {
			out[k] = []string{"****"}
			continue
		}
		vv := make([]string, len(v))
		copy(vv, v)
		out[k] = vv
	}
	return out
}

func captureBody(body []byte, contentType string) interface{} {
	if len(body) == 0 {
		return nil
	}
	trimmed := strings.TrimSpace(string(body))
	if strings.Contains(strings.ToLower(contentType), "json") || strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		var parsed interface{}
		if err := json.Unmarshal(body, &parsed); err == nil {
			return parsed
		}
	}
	return string(body)
}

func marshalCaptureDetail(detail map[string]interface{}) string {
	b, err := json.Marshal(detail)
	if err != nil {
		return fmt.Sprintf("%v", detail)
	}
	return string(b)
}

func (s *Server) resolveClassifierRoute(pre *Preflight) *router.Route {
	if route := s.router.ResolveClassifier(); route != nil {
		return route
	}

	if model := s.mainModelForConversation(pre.ConversationID); model != "" {
		if route := s.router.ResolveByName(model); route != nil {
			return route
		}
		if route := s.router.Resolve(model); route != nil {
			return route
		}
	}

	if route := s.router.ResolveByName(pre.SourceModel); route != nil {
		return route
	}
	return s.router.Resolve(pre.SourceModel)
}

func (s *Server) rememberMainModel(pre *Preflight, route *router.Route) {
	if pre == nil || pre.IsClassifier || route == nil || route.Model == nil {
		return
	}

	modelName := route.Model.Name
	if modelName == "" {
		modelName = route.Model.ModelID
	}
	s.rememberModelName(pre, modelName)
}

func (s *Server) rememberEasterEggModel(pre *Preflight) {
	s.rememberModelName(pre, "GPT-8.8")
}

func (s *Server) rememberModelName(pre *Preflight, modelName string) {
	if pre == nil || modelName == "" {
		return
	}

	s.classifierMu.Lock()
	defer s.classifierMu.Unlock()

	if pre.ConversationID != "" {
		s.conversationModels[pre.ConversationID] = modelName
	}
	s.lastMainModel = modelName
}

func (s *Server) mainModelForConversation(conversationID string) string {
	s.classifierMu.RLock()
	defer s.classifierMu.RUnlock()

	if conversationID != "" {
		if model := s.conversationModels[conversationID]; model != "" {
			return model
		}
	}
	return s.lastMainModel
}
