// daily_stats.go
// 按日期+模型的用量统计持久化

package store

// UpsertDailyStats 插入或累加日统计记录
func (db *DB) UpsertDailyStats(ds *DailyModelStats) error {
	_, err := db.conn.Exec(`
		INSERT INTO daily_stats (date, model_id, requests, errors, input_tokens, output_tokens, cache_read, cache_write, credits)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(date, model_id) DO UPDATE SET
			requests = requests + excluded.requests,
			errors = errors + excluded.errors,
			input_tokens = input_tokens + excluded.input_tokens,
			output_tokens = output_tokens + excluded.output_tokens,
			cache_read = cache_read + excluded.cache_read,
			cache_write = cache_write + excluded.cache_write,
			credits = credits + excluded.credits
	`, ds.Date, ds.ModelID, ds.Requests, ds.Errors,
		ds.InputTokens, ds.OutputTokens, ds.CacheRead, ds.CacheWrite, ds.Credits)
	return err
}

// QueryDailyStats 查询指定日期范围的统计数据
func (db *DB) QueryDailyStats(startDate, endDate string) ([]DailyModelStats, error) {
	rows, err := db.conn.Query(`
		SELECT date, model_id, requests, errors, input_tokens, output_tokens, cache_read, cache_write, credits,
			generation_speed_v2_sum, generation_speed_v2_samples
		FROM daily_stats
		WHERE date >= ? AND date <= ?
		ORDER BY date DESC, model_id ASC
	`, startDate, endDate)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []DailyModelStats
	for rows.Next() {
		var ds DailyModelStats
		if err := rows.Scan(&ds.Date, &ds.ModelID, &ds.Requests, &ds.Errors,
			&ds.InputTokens, &ds.OutputTokens, &ds.CacheRead, &ds.CacheWrite, &ds.Credits,
			&ds.GenerationSpeedSum, &ds.GenerationSpeedSamples); err != nil {
			return nil, err
		}
		results = append(results, ds)
	}
	return results, rows.Err()
}

// ClearDailyStats 清空所有按日模型统计。
func (db *DB) ClearDailyStats() error {
	_, err := db.conn.Exec(`DELETE FROM daily_stats`)
	return err
}

// ClearUsageStats 在同一事务中清空请求明细和按日模型统计。
func (db *DB) ClearUsageStats() error {
	if db == nil {
		return nil
	}
	tx, err := db.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM request_usage`); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM daily_stats`); err != nil {
		return err
	}
	return tx.Commit()
}
