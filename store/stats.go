// stats.go
// 内存原子计数器 + 定期持久化到数据库
// 读取走内存（O(1)），写入异步批量刷盘，不阻塞请求热路径
//
// 新增：按日期+模型分统计，缓存 token 计数

package store

import (
	"errors"
	"log"
	"math"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// Stats 全局统计计数器
type Stats struct {
	TotalRequests atomic.Int64
	TotalErrors   atomic.Int64
	InputTokens   atomic.Int64
	OutputTokens  atomic.Int64
	CacheRead     atomic.Int64  // 缓存命中的输入 token
	CacheWrite    atomic.Int64  // 缓存写入的输入 token
	CreditsBits   atomic.Uint64 // float64 bits
	startTime     time.Time
	db            *DB
	revision      atomic.Uint64
	flushed       atomic.Uint64
	flushMu       sync.Mutex
	recordMu      sync.RWMutex
	pendingClear  atomic.Bool
	stop          chan struct{}
	workerWG      sync.WaitGroup
	closeOnce     sync.Once

	// 按日期+模型的详细统计（内存中维护，定期刷盘）
	dailyMu    sync.Mutex
	dailyStats map[string]*DailyModelStats // key: "2024-01-15|model_id"
}

// DailyModelStats 单日单模型的统计
type DailyModelStats struct {
	Date                   string  `json:"date"`
	ModelID                string  `json:"model_id"`
	Requests               int64   `json:"requests"`
	Errors                 int64   `json:"errors"`
	InputTokens            int64   `json:"input_tokens"`
	OutputTokens           int64   `json:"output_tokens"`
	CacheRead              int64   `json:"cache_read_tokens"`
	CacheWrite             int64   `json:"cache_write_tokens"`
	Credits                float64 `json:"credits"`
	GenerationSpeedSum     float64 `json:"generation_speed_sum"`
	GenerationSpeedSamples int64   `json:"generation_speed_samples"`
}

// NewStats 创建统计实例，从数据库恢复历史累计值
func NewStats(db *DB) *Stats {
	s := &Stats{
		startTime:  time.Now(),
		db:         db,
		dailyStats: make(map[string]*DailyModelStats),
		stop:       make(chan struct{}),
	}
	s.loadFromDB()
	s.startAutoSave()
	return s
}

// loadFromDB 启动时从数据库恢复累计统计
func (s *Stats) loadFromDB() {
	if s.db == nil {
		return
	}
	s.TotalRequests.Store(s.readInt("stat_total_requests"))
	s.TotalErrors.Store(s.readInt("stat_total_errors"))
	s.InputTokens.Store(s.readInt("stat_input_tokens"))
	s.OutputTokens.Store(s.readInt("stat_output_tokens"))
	s.CacheRead.Store(s.readInt("stat_cache_read"))
	s.CacheWrite.Store(s.readInt("stat_cache_write"))
	s.storeCredits(s.readFloat("stat_credits"))
}

func (s *Stats) readInt(key string) int64 {
	v, err := s.db.GetSetting(key)
	if err != nil || v == "" {
		return 0
	}
	n, _ := strconv.ParseInt(v, 10, 64)
	return n
}

func (s *Stats) readFloat(key string) float64 {
	v, err := s.db.GetSetting(key)
	if err != nil || v == "" {
		return 0
	}
	n, _ := strconv.ParseFloat(v, 64)
	return n
}

func (s *Stats) loadCredits() float64 {
	return math.Float64frombits(s.CreditsBits.Load())
}

func (s *Stats) storeCredits(v float64) {
	s.CreditsBits.Store(math.Float64bits(v))
}

func (s *Stats) addCredits(v float64) {
	if v == 0 {
		return
	}
	for {
		oldBits := s.CreditsBits.Load()
		old := math.Float64frombits(oldBits)
		newBits := math.Float64bits(old + v)
		if s.CreditsBits.CompareAndSwap(oldBits, newBits) {
			return
		}
	}
}

// startAutoSave 每 5 秒检查一次，有变更就刷盘
func (s *Stats) startAutoSave() {
	if s.db == nil {
		return
	}
	s.workerWG.Add(1)
	go func() {
		defer s.workerWG.Done()
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if s.revision.Load() != s.flushed.Load() {
					s.Flush()
				}
			case <-s.stop:
				return
			}
		}
	}()
}

// Flush 立即持久化当前统计
func (s *Stats) Flush() {
	if s.db == nil {
		return
	}
	s.flushMu.Lock()
	defer s.flushMu.Unlock()
	revision := s.revision.Load()
	if s.pendingClear.Load() {
		if err := s.db.ClearUsageStats(); err != nil {
			log.Printf("[stats] 清空用量统计重试失败，将在下次周期重试: %v", err)
			return
		}
		s.pendingClear.Store(false)
	}

	settings := map[string]string{
		"stat_total_requests": strconv.FormatInt(s.TotalRequests.Load(), 10),
		"stat_total_errors":   strconv.FormatInt(s.TotalErrors.Load(), 10),
		"stat_input_tokens":   strconv.FormatInt(s.InputTokens.Load(), 10),
		"stat_output_tokens":  strconv.FormatInt(s.OutputTokens.Load(), 10),
		"stat_cache_read":     strconv.FormatInt(s.CacheRead.Load(), 10),
		"stat_cache_write":    strconv.FormatInt(s.CacheWrite.Load(), 10),
		"stat_credits":        strconv.FormatFloat(s.loadCredits(), 'g', -1, 64),
	}
	var flushErr error
	for key, value := range settings {
		if err := s.db.SetSetting(key, value); err != nil {
			flushErr = errors.Join(flushErr, err)
		}
	}

	// 刷盘日统计
	flushErr = errors.Join(flushErr, s.flushDailyStats())
	if flushErr != nil {
		log.Printf("[stats] 持久化失败，将在下次周期重试: %v", flushErr)
		return
	}
	s.flushed.Store(revision)
}

func (s *Stats) Close() {
	s.closeOnce.Do(func() {
		close(s.stop)
		s.workerWG.Wait()
		s.Flush()
	})
}

// RecordRequest 记录一次请求完成（含缓存 token）
func (s *Stats) RecordRequest(inputTokens, outputTokens int, isError bool) {
	s.RecordRequestFull(inputTokens, outputTokens, 0, 0, "", isError)
}

// RecordRequestFull 完整记录一次请求（含缓存 token 和模型信息）
func (s *Stats) RecordRequestFull(inputTokens, outputTokens, cacheRead, cacheWrite int, modelID string, isError bool) {
	s.RecordRequestFullWithCredits(inputTokens, outputTokens, cacheRead, cacheWrite, 0, modelID, isError)
}

func (s *Stats) RecordRequestFullWithCredits(inputTokens, outputTokens, cacheRead, cacheWrite int, credits float64, modelID string, isError bool) {
	s.recordMu.RLock()
	defer s.recordMu.RUnlock()

	s.TotalRequests.Add(1)
	if isError {
		s.TotalErrors.Add(1)
	}
	s.InputTokens.Add(int64(inputTokens))
	s.OutputTokens.Add(int64(outputTokens))
	s.CacheRead.Add(int64(cacheRead))
	s.CacheWrite.Add(int64(cacheWrite))
	s.addCredits(credits)

	// 按日期+模型记录
	if modelID != "" {
		date := statsDay(time.Now())
		key := date + "|" + modelID

		s.dailyMu.Lock()
		ds, ok := s.dailyStats[key]
		if !ok {
			ds = &DailyModelStats{Date: date, ModelID: modelID}
			s.dailyStats[key] = ds
		}
		ds.Requests++
		if isError {
			ds.Errors++
		}
		ds.InputTokens += int64(inputTokens)
		ds.OutputTokens += int64(outputTokens)
		ds.CacheRead += int64(cacheRead)
		ds.CacheWrite += int64(cacheWrite)
		ds.Credits += credits
		s.dailyMu.Unlock()
	}
	s.revision.Add(1)
}

// flushDailyStats 将内存中的增量日统计写入数据库
func (s *Stats) flushDailyStats() error {
	today := statsDay(time.Now())

	s.dailyMu.Lock()
	snapshot := make(map[string]*DailyModelStats, len(s.dailyStats))
	for k, v := range s.dailyStats {
		if v.Requests == 0 && v.Errors == 0 && v.InputTokens == 0 &&
			v.OutputTokens == 0 && v.CacheRead == 0 && v.CacheWrite == 0 && v.Credits == 0 &&
			v.GenerationSpeedSum == 0 && v.GenerationSpeedSamples == 0 {
			// 清理非今日的空条目，避免 map 无限增长
			if v.Date != today {
				delete(s.dailyStats, k)
			}
			continue
		}
		cp := *v
		snapshot[k] = &cp

		// 原 map 只保留后续新增的增量，避免下次刷盘重复累加同一批数据。
		v.Requests = 0
		v.Errors = 0
		v.InputTokens = 0
		v.OutputTokens = 0
		v.CacheRead = 0
		v.CacheWrite = 0
		v.Credits = 0
	}
	s.dailyMu.Unlock()

	var flushErr error
	for key, ds := range snapshot {
		if err := s.db.UpsertDailyStats(ds); err != nil {
			flushErr = errors.Join(flushErr, err)
			s.dailyMu.Lock()
			current := s.dailyStats[key]
			if current == nil {
				copyStats := *ds
				s.dailyStats[key] = &copyStats
			} else {
				current.Requests += ds.Requests
				current.Errors += ds.Errors
				current.InputTokens += ds.InputTokens
				current.OutputTokens += ds.OutputTokens
				current.CacheRead += ds.CacheRead
				current.CacheWrite += ds.CacheWrite
				current.Credits += ds.Credits
			}
			s.dailyMu.Unlock()
		}
	}
	return flushErr
}

// Snapshot 返回当前统计快照
func (s *Stats) Snapshot() StatsSnapshot {
	inputTokens := s.InputTokens.Load()
	outputTokens := s.OutputTokens.Load()
	cacheRead := s.CacheRead.Load()
	today := s.TodayStats()
	return StatsSnapshot{
		TotalRequests:     s.TotalRequests.Load(),
		TotalErrors:       s.TotalErrors.Load(),
		InputTokens:       inputTokens,
		OutputTokens:      outputTokens,
		CacheRead:         cacheRead,
		CacheWrite:        s.CacheWrite.Load(),
		Credits:           s.loadCredits(),
		CacheHitRatio:     cacheHitRatio(cacheRead, inputTokens),
		TotalTokens:       inputTokens + outputTokens,
		UptimeSeconds:     int64(time.Since(s.startTime).Seconds()),
		TodayRequests:     today.Requests,
		TodayErrors:       today.Errors,
		TodayInputTokens:  today.InputTokens,
		TodayOutputTokens: today.OutputTokens,
		TodayCacheRead:    today.CacheRead,
		TodayCacheWrite:   today.CacheWrite,
		TodayCredits:      today.Credits,
		TodayTotalTokens:  today.InputTokens + today.OutputTokens,
	}
}

func cacheHitRatio(cacheRead, inputTokens int64) float64 {
	if inputTokens <= 0 || cacheRead <= 0 {
		return 0
	}
	if cacheRead > inputTokens {
		return 1
	}
	return float64(cacheRead) / float64(inputTokens)
}

// Clear 清空累计统计和按日模型统计。
func (s *Stats) Clear() error {
	s.flushMu.Lock()
	defer s.flushMu.Unlock()
	s.recordMu.Lock()
	defer s.recordMu.Unlock()

	s.TotalRequests.Store(0)
	s.TotalErrors.Store(0)
	s.InputTokens.Store(0)
	s.OutputTokens.Store(0)
	s.CacheRead.Store(0)
	s.CacheWrite.Store(0)
	s.storeCredits(0)

	s.dailyMu.Lock()
	s.dailyStats = make(map[string]*DailyModelStats)
	s.dailyMu.Unlock()
	revision := s.revision.Add(1)

	if s.db == nil {
		s.pendingClear.Store(false)
		s.flushed.Store(revision)
		return nil
	}
	s.pendingClear.Store(true)

	keys := []string{
		"stat_total_requests",
		"stat_total_errors",
		"stat_input_tokens",
		"stat_output_tokens",
		"stat_cache_read",
		"stat_cache_write",
		"stat_credits",
	}
	for _, key := range keys {
		if err := s.db.SetSetting(key, "0"); err != nil {
			return err
		}
	}
	if err := s.db.ClearUsageStats(); err != nil {
		return err
	}
	s.pendingClear.Store(false)
	s.flushed.Store(revision)
	return nil
}

// GetDailyStats 获取指定日期范围的按模型统计，合并数据库与尚未刷盘的内存增量。
func (s *Stats) GetDailyStats(startDate, endDate string) []DailyModelStats {
	merged := make(map[string]*DailyModelStats)
	if s.db != nil {
		results, _ := s.db.QueryDailyStats(startDate, endDate)
		for _, row := range results {
			cp := row
			merged[row.Date+"|"+row.ModelID] = &cp
		}
	}

	s.dailyMu.Lock()
	for _, row := range s.dailyStats {
		if row.Date < startDate || row.Date > endDate {
			continue
		}
		key := row.Date + "|" + row.ModelID
		cur, ok := merged[key]
		if !ok {
			cp := DailyModelStats{Date: row.Date, ModelID: row.ModelID}
			cur = &cp
			merged[key] = cur
		}
		cur.Requests += row.Requests
		cur.Errors += row.Errors
		cur.InputTokens += row.InputTokens
		cur.OutputTokens += row.OutputTokens
		cur.CacheRead += row.CacheRead
		cur.CacheWrite += row.CacheWrite
		cur.Credits += row.Credits
		cur.GenerationSpeedSum += row.GenerationSpeedSum
		cur.GenerationSpeedSamples += row.GenerationSpeedSamples
	}
	s.dailyMu.Unlock()

	results := make([]DailyModelStats, 0, len(merged))
	for _, row := range merged {
		if row.Requests == 0 && row.Errors == 0 && row.InputTokens == 0 && row.OutputTokens == 0 &&
			row.CacheRead == 0 && row.CacheWrite == 0 && row.Credits == 0 &&
			row.GenerationSpeedSum == 0 && row.GenerationSpeedSamples == 0 {
			continue
		}
		results = append(results, *row)
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].Date == results[j].Date {
			return results[i].ModelID < results[j].ModelID
		}
		return results[i].Date > results[j].Date
	})
	return results
}

// TodayStats 返回按本地时间自然日（00:00）分界的当日聚合统计。
func (s *Stats) TodayStats() DailyModelStats {
	today := statsDay(time.Now())
	var total DailyModelStats
	total.Date = today
	for _, row := range s.GetDailyStats(today, today) {
		total.Requests += row.Requests
		total.Errors += row.Errors
		total.InputTokens += row.InputTokens
		total.OutputTokens += row.OutputTokens
		total.CacheRead += row.CacheRead
		total.CacheWrite += row.CacheWrite
		total.Credits += row.Credits
		total.GenerationSpeedSum += row.GenerationSpeedSum
		total.GenerationSpeedSamples += row.GenerationSpeedSamples
	}
	return total
}

// TodayCredits 返回按本地时间自然日（00:00）分界的当日 Credit 用量。
func (s *Stats) TodayCredits() float64 {
	return s.TodayStats().Credits
}

// MonthlyCredits 返回本地自然月的累计值，包括未刷盘增量。
// 与刷盘互斥，防止在查询数据库与内存之间丢失或重复计算一批增量。
func (s *Stats) MonthlyCredits(now time.Time) (float64, error) {
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	startDate, endDate := statsDay(start), statsDay(now)
	s.flushMu.Lock()
	defer s.flushMu.Unlock()
	var total float64
	if s.db != nil {
		rows, err := s.db.QueryDailyStats(startDate, endDate)
		if err != nil {
			return 0, err
		}
		for _, row := range rows {
			total += row.Credits
		}
	}
	s.dailyMu.Lock()
	defer s.dailyMu.Unlock()
	for _, row := range s.dailyStats {
		if row.Date >= startDate && row.Date <= endDate {
			total += row.Credits
		}
	}
	return total, nil
}

type StatsSnapshot struct {
	TotalRequests     int64   `json:"total_requests"`
	TotalErrors       int64   `json:"total_errors"`
	InputTokens       int64   `json:"input_tokens"`
	OutputTokens      int64   `json:"output_tokens"`
	CacheRead         int64   `json:"cache_read_tokens"`
	CacheWrite        int64   `json:"cache_write_tokens"`
	Credits           float64 `json:"credits"`
	CacheHitRatio     float64 `json:"cache_hit_ratio"`
	TotalTokens       int64   `json:"total_tokens"`
	UptimeSeconds     int64   `json:"uptime_seconds"`
	TodayRequests     int64   `json:"today_requests"`
	TodayErrors       int64   `json:"today_errors"`
	TodayInputTokens  int64   `json:"today_input_tokens"`
	TodayOutputTokens int64   `json:"today_output_tokens"`
	TodayCacheRead    int64   `json:"today_cache_read_tokens"`
	TodayCacheWrite   int64   `json:"today_cache_write_tokens"`
	TodayCredits      float64 `json:"today_credits"`
	TodayTotalTokens  int64   `json:"today_total_tokens"`
}

func statsDay(t time.Time) string {
	return t.Format("2006-01-02")
}

// StatsDay 返回按本地时间自然日（00:00）分界的日期。
// 对外提供统一日期口径，供 API 和存储查询共同使用。
func StatsDay(t time.Time) string {
	return statsDay(t)
}
