package store

import (
	"fmt"
	"log"
	"strings"
	"time"
)

// RequestUsage 是单次上游请求的结构化统计，不保存请求或响应正文。
type RequestUsage struct {
	ID                         int64   `json:"id"`
	RequestDate                string  `json:"request_date"`
	CreatedAt                  string  `json:"created_at"`
	ModelID                    string  `json:"model_id"`
	Provider                   string  `json:"provider"`
	Endpoint                   string  `json:"endpoint"`
	RequestPath                string  `json:"request_path"`
	Protocol                   string  `json:"protocol"`
	StatusCode                 int     `json:"status_code"`
	Success                    bool    `json:"success"`
	InputTokens                int     `json:"input_tokens"`
	OutputTokens               int     `json:"output_tokens"`
	CacheReadTokens            int     `json:"cache_read_tokens"`
	CacheWriteTokens           int     `json:"cache_write_tokens"`
	FirstByteMS                int64   `json:"first_byte_ms"`
	FirstEventMS               int64   `json:"first_event_ms"`
	FirstContentMS             int64   `json:"first_content_ms"`
	FirstContentType           string  `json:"first_content_type"`
	MaxContentGapMS            int64   `json:"max_content_gap_ms"`
	ContentMeasured            bool    `json:"content_measured"`
	ContentMeasurementComplete bool    `json:"content_measurement_complete"`
	DurationMS                 int64   `json:"duration_ms"`
	Credits                    float64 `json:"credits"`
	InputCost                  float64 `json:"input_cost"`
	OutputCost                 float64 `json:"output_cost"`
	CacheReadCost              float64 `json:"cache_read_cost"`
	CacheWriteCost             float64 `json:"cache_write_cost"`
	ConversionMS               int64   `json:"conversion_ms"`
	AuditMS                    int64   `json:"audit_ms"`
	SessionReadMS              int64   `json:"session_read_ms"`
	AuthMS                     int64   `json:"auth_ms"`
	DNSMS                      int64   `json:"dns_ms"`
	ConnectMS                  int64   `json:"connect_ms"`
	TLSMS                      int64   `json:"tls_ms"`
	RequestSendMS              int64   `json:"request_send_ms"`
	UpstreamWaitMS             int64   `json:"upstream_wait_ms"`
	ResponseHeaderMS           int64   `json:"response_header_ms"`
	StreamWaitMS               int64   `json:"stream_wait_ms"`
	RetryCount                 int     `json:"retry_count"`
	ReusedConnection           bool    `json:"reused_connection"`
	WireAttemptCount           int     `json:"wire_attempt_count"`
	TransportFallbackCount     int     `json:"transport_fallback_count"`
	ReasoningEffort            string  `json:"reasoning_effort,omitempty"`
	ServiceTier                string  `json:"service_tier,omitempty"`
	RequestedModel             string  `json:"requested_model,omitempty"`
	RouteGroupID               string  `json:"route_group_id,omitempty"`
	SelectedModelConfigID      string  `json:"selected_model_config_id,omitempty"`
	FailoverCount              int     `json:"failover_count"`
	DispatchReason             string  `json:"dispatch_reason,omitempty"`
	FailoverReason             string  `json:"failover_reason,omitempty"`
	CapacityWaitMS             int64   `json:"capacity_wait_ms"`
	ErrorMessage               string  `json:"error_message,omitempty"`
}

type RequestUsageSummary struct {
	Requests            int64   `json:"requests"`
	Errors              int64   `json:"errors"`
	InputTokens         int64   `json:"input_tokens"`
	OutputTokens        int64   `json:"output_tokens"`
	CacheReadTokens     int64   `json:"cache_read_tokens"`
	CacheWriteTokens    int64   `json:"cache_write_tokens"`
	Credits             float64 `json:"credits"`
	CostUSD             float64 `json:"cost_usd"`
	AverageDuration     int64   `json:"average_duration_ms"`
	AverageFirstByte    int64   `json:"average_first_byte_ms"`
	AverageFirstContent int64   `json:"average_first_content_ms"`
	ContentSamples      int64   `json:"content_samples"`
}

type RequestUsageQuery struct {
	StartDate string
	EndDate   string
	Keyword   string
	Provider  string
	Status    string
	Page      int
	PageSize  int
}

type RequestUsagePage struct {
	Items      []RequestUsage
	Summary    RequestUsageSummary
	Page       int
	PageSize   int
	Total      int64
	TotalPages int
}

const requestUsageRetentionDays = 7

// cleanupExpiredRequestUsage 保留当前自然日及之前 6 个自然日的数据，每天最多执行一次。
func (db *DB) cleanupExpiredRequestUsage(now time.Time) error {
	if db == nil {
		return nil
	}
	logicalNow := now
	day := int64(logicalNow.Year()*10000 + int(logicalNow.Month())*100 + logicalNow.Day())
	previousDay := db.requestUsageCleanupDay.Load()
	if previousDay == day || !db.requestUsageCleanupDay.CompareAndSwap(previousDay, day) {
		return nil
	}

	// request_date 与 daily_stats 使用同一套本地时间自然日（00:00）切日规则。
	cutoff := statsDay(now.AddDate(0, 0, -(requestUsageRetentionDays - 1)))
	if _, err := db.conn.Exec(`DELETE FROM request_usage WHERE request_date < ?`, cutoff); err != nil {
		db.requestUsageCleanupDay.CompareAndSwap(day, previousDay)
		return fmt.Errorf("删除 %s 之前的请求统计失败: %w", cutoff, err)
	}
	return nil
}

func (db *DB) InsertRequestUsage(item *RequestUsage) error {
	if db == nil || item == nil {
		return nil
	}
	now := time.Now()
	if err := db.cleanupExpiredRequestUsage(now); err != nil {
		log.Printf("[store] 清理过期请求统计失败: %v", err)
	}
	if item.CreatedAt == "" {
		item.CreatedAt = now.Format(time.RFC3339Nano)
	}
	if item.RequestDate == "" {
		item.RequestDate = statsDay(now)
	}
	_, err := db.conn.Exec(`
		INSERT INTO request_usage (
			request_date, created_at, model_id, provider, endpoint, request_path, protocol,
			status_code, success, input_tokens, output_tokens, cache_read_tokens,
			cache_write_tokens, first_byte_ms, first_event_ms, first_content_ms, first_content_type,
			max_content_gap_ms, content_measured, content_measurement_complete, duration_ms, credits, input_cost, output_cost,
			cache_read_cost, cache_write_cost, conversion_ms, audit_ms, session_read_ms, auth_ms, dns_ms, connect_ms,
			tls_ms, request_send_ms, upstream_wait_ms, response_header_ms, stream_wait_ms,
			retry_count, reused_connection, wire_attempt_count, transport_fallback_count, reasoning_effort, service_tier, requested_model,
			route_group_id, selected_model_config_id, failover_count, dispatch_reason,
			failover_reason, capacity_wait_ms, error_message
		) VALUES (
			?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
			?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
			?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
			?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
			, ?, ?, ?, ?, ?
		)
	`, item.RequestDate, item.CreatedAt, item.ModelID, item.Provider, item.Endpoint,
		item.RequestPath, item.Protocol, item.StatusCode, boolToInt(item.Success),
		item.InputTokens, item.OutputTokens, item.CacheReadTokens, item.CacheWriteTokens,
		item.FirstByteMS, item.FirstEventMS, item.FirstContentMS, item.FirstContentType,
		item.MaxContentGapMS, boolToInt(item.ContentMeasured), boolToInt(item.ContentMeasurementComplete),
		item.DurationMS, item.Credits, item.InputCost, item.OutputCost,
		item.CacheReadCost, item.CacheWriteCost, item.ConversionMS, item.AuditMS, item.SessionReadMS, item.AuthMS, item.DNSMS,
		item.ConnectMS, item.TLSMS, item.RequestSendMS, item.UpstreamWaitMS,
		item.ResponseHeaderMS, item.StreamWaitMS, item.RetryCount, boolToInt(item.ReusedConnection), item.WireAttemptCount, item.TransportFallbackCount, item.ReasoningEffort,
		item.ServiceTier, item.RequestedModel, item.RouteGroupID, item.SelectedModelConfigID,
		item.FailoverCount, item.DispatchReason, item.FailoverReason, item.CapacityWaitMS, item.ErrorMessage)
	return err
}

// ClearRequestUsage 清空所有单次请求统计。
func (db *DB) ClearRequestUsage() error {
	if db == nil {
		return nil
	}
	_, err := db.conn.Exec(`DELETE FROM request_usage`)
	return err
}

func (db *DB) QueryRequestUsage(startDate, endDate string, limit int) ([]RequestUsage, RequestUsageSummary, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	result, err := db.QueryRequestUsagePage(RequestUsageQuery{
		StartDate: startDate,
		EndDate:   endDate,
		PageSize:  limit,
	})
	if err != nil {
		return nil, RequestUsageSummary{}, err
	}
	return result.Items, result.Summary, nil
}

func (db *DB) QueryRequestUsagePage(query RequestUsageQuery) (RequestUsagePage, error) {
	page := query.Page
	if page < 1 {
		page = 1
	}
	pageSize := query.PageSize
	if pageSize <= 0 {
		pageSize = 50
	}
	if pageSize > 200 {
		pageSize = 200
	}

	where, args := requestUsageWhere(query)
	var total int64
	if err := db.conn.QueryRow(`SELECT COUNT(*) FROM request_usage`+where, args...).Scan(&total); err != nil {
		return RequestUsagePage{}, fmt.Errorf("查询请求统计总数失败: %w", err)
	}

	totalPages := 0
	if total > 0 {
		totalPages = int((total + int64(pageSize) - 1) / int64(pageSize))
		if page > totalPages {
			page = totalPages
		}
	} else {
		page = 1
	}
	offset := (page - 1) * pageSize
	rows, err := db.conn.Query(`
		SELECT id, request_date, created_at, model_id, provider, endpoint, request_path,
			protocol, status_code, success, input_tokens, output_tokens, cache_read_tokens,
			cache_write_tokens, first_byte_ms, first_event_ms, first_content_ms, first_content_type,
			max_content_gap_ms, content_measured, content_measurement_complete, duration_ms, credits, input_cost, output_cost,
			cache_read_cost, cache_write_cost, conversion_ms, audit_ms, session_read_ms, auth_ms, dns_ms, connect_ms,
			tls_ms, request_send_ms, upstream_wait_ms, response_header_ms, stream_wait_ms,
			retry_count, reused_connection, wire_attempt_count, transport_fallback_count, reasoning_effort, service_tier, requested_model,
			route_group_id, selected_model_config_id, failover_count, dispatch_reason,
			failover_reason, capacity_wait_ms, error_message
		FROM request_usage`+where+` ORDER BY julianday(created_at) DESC, id DESC LIMIT ? OFFSET ?`, append(args, pageSize, offset)...)
	if err != nil {
		return RequestUsagePage{}, err
	}
	defer rows.Close()

	items := make([]RequestUsage, 0, pageSize)
	for rows.Next() {
		var item RequestUsage
		var success, reusedConnection, contentMeasured, measurementComplete int
		if err := rows.Scan(&item.ID, &item.RequestDate, &item.CreatedAt, &item.ModelID,
			&item.Provider, &item.Endpoint, &item.RequestPath, &item.Protocol, &item.StatusCode,
			&success, &item.InputTokens, &item.OutputTokens, &item.CacheReadTokens,
			&item.CacheWriteTokens, &item.FirstByteMS, &item.FirstEventMS, &item.FirstContentMS, &item.FirstContentType,
			&item.MaxContentGapMS, &contentMeasured, &measurementComplete, &item.DurationMS, &item.Credits,
			&item.InputCost, &item.OutputCost, &item.CacheReadCost, &item.CacheWriteCost,
			&item.ConversionMS, &item.AuditMS, &item.SessionReadMS, &item.AuthMS, &item.DNSMS, &item.ConnectMS, &item.TLSMS,
			&item.RequestSendMS, &item.UpstreamWaitMS, &item.ResponseHeaderMS,
			&item.StreamWaitMS, &item.RetryCount, &reusedConnection, &item.WireAttemptCount, &item.TransportFallbackCount, &item.ReasoningEffort, &item.ServiceTier,
			&item.RequestedModel, &item.RouteGroupID, &item.SelectedModelConfigID, &item.FailoverCount,
			&item.DispatchReason, &item.FailoverReason, &item.CapacityWaitMS, &item.ErrorMessage); err != nil {
			return RequestUsagePage{}, err
		}
		item.Success = success != 0
		item.ReusedConnection = reusedConnection != 0
		item.ContentMeasured = contentMeasured != 0
		item.ContentMeasurementComplete = measurementComplete != 0
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return RequestUsagePage{}, err
	}
	if err := rows.Close(); err != nil {
		return RequestUsagePage{}, err
	}

	var summary RequestUsageSummary
	err = db.conn.QueryRow(`
		SELECT COUNT(*), COALESCE(SUM(CASE WHEN success = 0 THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE
				WHEN input_tokens < cache_read_tokens + cache_write_tokens
				THEN input_tokens + cache_read_tokens + cache_write_tokens
				ELSE input_tokens
			END), 0), COALESCE(SUM(output_tokens), 0),
			COALESCE(SUM(cache_read_tokens), 0), COALESCE(SUM(cache_write_tokens), 0),
			COALESCE(SUM(credits), 0), COALESCE(SUM(input_cost), 0) + COALESCE(SUM(output_cost), 0) +
				COALESCE(SUM(cache_read_cost), 0) + COALESCE(SUM(cache_write_cost), 0),
			CAST(COALESCE(AVG(duration_ms), 0) AS INTEGER),
			CAST(COALESCE(AVG(CASE WHEN first_byte_ms > 0 THEN first_byte_ms END), 0) AS INTEGER),
			CAST(COALESCE(AVG(CASE WHEN content_measured != 0 AND content_measurement_complete != 0 THEN first_content_ms END), 0) AS INTEGER),
			COALESCE(SUM(CASE WHEN content_measured != 0 AND content_measurement_complete != 0 THEN 1 ELSE 0 END), 0)
		FROM request_usage`+where, args...).Scan(&summary.Requests, &summary.Errors,
		&summary.InputTokens, &summary.OutputTokens, &summary.CacheReadTokens,
		&summary.CacheWriteTokens, &summary.Credits, &summary.CostUSD, &summary.AverageDuration, &summary.AverageFirstByte,
		&summary.AverageFirstContent, &summary.ContentSamples)
	if err != nil {
		return RequestUsagePage{}, fmt.Errorf("查询请求统计汇总失败: %w", err)
	}

	return RequestUsagePage{
		Items:      items,
		Summary:    summary,
		Page:       page,
		PageSize:   pageSize,
		Total:      total,
		TotalPages: totalPages,
	}, nil
}

func requestUsageWhere(query RequestUsageQuery) (string, []interface{}) {
	conditions := make([]string, 0, 5)
	args := make([]interface{}, 0, 5)
	if strings.TrimSpace(query.StartDate) != "" {
		conditions = append(conditions, "request_date >= ?")
		args = append(args, strings.TrimSpace(query.StartDate))
	}
	if strings.TrimSpace(query.EndDate) != "" {
		conditions = append(conditions, "request_date <= ?")
		args = append(args, strings.TrimSpace(query.EndDate))
	}
	if keyword := strings.TrimSpace(query.Keyword); keyword != "" {
		pattern := "%" + strings.ToLower(keyword) + "%"
		conditions = append(conditions, `(LOWER(model_id) LIKE ? OR LOWER(provider) LIKE ? OR LOWER(endpoint) LIKE ? OR LOWER(request_path) LIKE ? OR LOWER(requested_model) LIKE ? OR LOWER(error_message) LIKE ?)`)
		args = append(args, pattern, pattern, pattern, pattern, pattern, pattern)
	}
	if provider := strings.TrimSpace(query.Provider); provider != "" {
		conditions = append(conditions, "LOWER(provider) = ?")
		args = append(args, strings.ToLower(provider))
	}
	switch strings.ToLower(strings.TrimSpace(query.Status)) {
	case "success":
		conditions = append(conditions, "success = 1")
	case "error":
		conditions = append(conditions, "success = 0")
	}
	if len(conditions) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(conditions, " AND "), args
}
