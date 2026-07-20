package store

import (
	"fmt"
	"log"
	"strings"
	"time"
)

// RequestUsage 是单次上游请求的结构化统计，不保存请求或响应正文。
type RequestUsage struct {
	ID               int64   `json:"id"`
	RequestDate      string  `json:"request_date"`
	CreatedAt        string  `json:"created_at"`
	ModelID          string  `json:"model_id"`
	Provider         string  `json:"provider"`
	Endpoint         string  `json:"endpoint"`
	RequestPath      string  `json:"request_path"`
	Protocol         string  `json:"protocol"`
	StatusCode       int     `json:"status_code"`
	Success          bool    `json:"success"`
	InputTokens      int     `json:"input_tokens"`
	OutputTokens     int     `json:"output_tokens"`
	CacheReadTokens  int     `json:"cache_read_tokens"`
	CacheWriteTokens int     `json:"cache_write_tokens"`
	FirstByteMS      int64   `json:"first_byte_ms"`
	DurationMS       int64   `json:"duration_ms"`
	Credits          float64 `json:"credits"`
	InputCost        float64 `json:"input_cost"`
	OutputCost       float64 `json:"output_cost"`
	CacheReadCost    float64 `json:"cache_read_cost"`
	CacheWriteCost   float64 `json:"cache_write_cost"`
	ConversionMS     int64   `json:"conversion_ms"`
	AuthMS           int64   `json:"auth_ms"`
	DNSMS            int64   `json:"dns_ms"`
	ConnectMS        int64   `json:"connect_ms"`
	TLSMS            int64   `json:"tls_ms"`
	RequestSendMS    int64   `json:"request_send_ms"`
	UpstreamWaitMS   int64   `json:"upstream_wait_ms"`
	ResponseHeaderMS int64   `json:"response_header_ms"`
	StreamWaitMS     int64   `json:"stream_wait_ms"`
	RetryCount       int     `json:"retry_count"`
	ReusedConnection bool    `json:"reused_connection"`
	ServiceTier      string  `json:"service_tier,omitempty"`
	ErrorMessage     string  `json:"error_message,omitempty"`
}

type RequestUsageSummary struct {
	Requests         int64   `json:"requests"`
	Errors           int64   `json:"errors"`
	InputTokens      int64   `json:"input_tokens"`
	OutputTokens     int64   `json:"output_tokens"`
	CacheReadTokens  int64   `json:"cache_read_tokens"`
	CacheWriteTokens int64   `json:"cache_write_tokens"`
	Credits          float64 `json:"credits"`
	AverageDuration  int64   `json:"average_duration_ms"`
	AverageFirstByte int64   `json:"average_first_byte_ms"`
}

const requestUsageRetentionDays = 7

// cleanupExpiredRequestUsage 保留今天及之前 6 个自然日的数据，每天最多执行一次。
func (db *DB) cleanupExpiredRequestUsage(now time.Time) error {
	if db == nil {
		return nil
	}
	day := int64(now.Year()*10000 + int(now.Month())*100 + now.Day())
	previousDay := db.requestUsageCleanupDay.Load()
	if previousDay == day || !db.requestUsageCleanupDay.CompareAndSwap(previousDay, day) {
		return nil
	}

	cutoff := now.AddDate(0, 0, -(requestUsageRetentionDays - 1)).Format("2006-01-02")
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
		item.RequestDate = now.Format("2006-01-02")
	}
	_, err := db.conn.Exec(`
		INSERT INTO request_usage (
			request_date, created_at, model_id, provider, endpoint, request_path, protocol,
			status_code, success, input_tokens, output_tokens, cache_read_tokens,
			cache_write_tokens, first_byte_ms, duration_ms, credits, input_cost, output_cost,
			cache_read_cost, cache_write_cost, conversion_ms, auth_ms, dns_ms, connect_ms,
			tls_ms, request_send_ms, upstream_wait_ms, response_header_ms, stream_wait_ms,
			retry_count, reused_connection, service_tier, error_message
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, item.RequestDate, item.CreatedAt, item.ModelID, item.Provider, item.Endpoint,
		item.RequestPath, item.Protocol, item.StatusCode, boolToInt(item.Success),
		item.InputTokens, item.OutputTokens, item.CacheReadTokens, item.CacheWriteTokens,
		item.FirstByteMS, item.DurationMS, item.Credits, item.InputCost, item.OutputCost,
		item.CacheReadCost, item.CacheWriteCost, item.ConversionMS, item.AuthMS, item.DNSMS,
		item.ConnectMS, item.TLSMS, item.RequestSendMS, item.UpstreamWaitMS,
		item.ResponseHeaderMS, item.StreamWaitMS, item.RetryCount, boolToInt(item.ReusedConnection),
		item.ServiceTier, item.ErrorMessage)
	return err
}

func (db *DB) QueryRequestUsage(startDate, endDate string, limit int) ([]RequestUsage, RequestUsageSummary, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	where, args := requestUsageWhere(startDate, endDate)
	rows, err := db.conn.Query(`
		SELECT id, request_date, created_at, model_id, provider, endpoint, request_path,
			protocol, status_code, success, input_tokens, output_tokens, cache_read_tokens,
			cache_write_tokens, first_byte_ms, duration_ms, credits, input_cost, output_cost,
			cache_read_cost, cache_write_cost, conversion_ms, auth_ms, dns_ms, connect_ms,
			tls_ms, request_send_ms, upstream_wait_ms, response_header_ms, stream_wait_ms,
			retry_count, reused_connection, service_tier, error_message
		FROM request_usage`+where+` ORDER BY id DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, RequestUsageSummary{}, err
	}
	defer rows.Close()

	items := make([]RequestUsage, 0, limit)
	for rows.Next() {
		var item RequestUsage
		var success, reusedConnection int
		if err := rows.Scan(&item.ID, &item.RequestDate, &item.CreatedAt, &item.ModelID,
			&item.Provider, &item.Endpoint, &item.RequestPath, &item.Protocol, &item.StatusCode,
			&success, &item.InputTokens, &item.OutputTokens, &item.CacheReadTokens,
			&item.CacheWriteTokens, &item.FirstByteMS, &item.DurationMS, &item.Credits,
			&item.InputCost, &item.OutputCost, &item.CacheReadCost, &item.CacheWriteCost,
			&item.ConversionMS, &item.AuthMS, &item.DNSMS, &item.ConnectMS, &item.TLSMS,
			&item.RequestSendMS, &item.UpstreamWaitMS, &item.ResponseHeaderMS,
			&item.StreamWaitMS, &item.RetryCount, &reusedConnection, &item.ServiceTier,
			&item.ErrorMessage); err != nil {
			return nil, RequestUsageSummary{}, err
		}
		item.Success = success != 0
		item.ReusedConnection = reusedConnection != 0
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, RequestUsageSummary{}, err
	}
	if err := rows.Close(); err != nil {
		return nil, RequestUsageSummary{}, err
	}

	var summary RequestUsageSummary
	err = db.conn.QueryRow(`
		SELECT COUNT(*), COALESCE(SUM(CASE WHEN success = 0 THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0),
			COALESCE(SUM(cache_read_tokens), 0), COALESCE(SUM(cache_write_tokens), 0),
			COALESCE(SUM(credits), 0), CAST(COALESCE(AVG(duration_ms), 0) AS INTEGER),
			CAST(COALESCE(AVG(CASE WHEN first_byte_ms > 0 THEN first_byte_ms END), 0) AS INTEGER)
		FROM request_usage`+where, args...).Scan(&summary.Requests, &summary.Errors,
		&summary.InputTokens, &summary.OutputTokens, &summary.CacheReadTokens,
		&summary.CacheWriteTokens, &summary.Credits, &summary.AverageDuration, &summary.AverageFirstByte)
	if err != nil {
		return nil, RequestUsageSummary{}, fmt.Errorf("查询请求统计汇总失败: %w", err)
	}
	return items, summary, nil
}

func requestUsageWhere(startDate, endDate string) (string, []interface{}) {
	conditions := make([]string, 0, 2)
	args := make([]interface{}, 0, 2)
	if strings.TrimSpace(startDate) != "" {
		conditions = append(conditions, "request_date >= ?")
		args = append(args, startDate)
	}
	if strings.TrimSpace(endDate) != "" {
		conditions = append(conditions, "request_date <= ?")
		args = append(args, endDate)
	}
	if len(conditions) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(conditions, " AND "), args
}
