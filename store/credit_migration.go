package store

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strconv"

	"kiro-proxy/billing"
)

const creditUnitVersionKey = "credit_unit_version"
const creditUnitVersion = "usd_per_credit_0.04_v1"

// migrateCreditUnits 在同一事务内保留旧值并转换单位；只在启动时执行。
func (db *DB) migrateCreditUnits() error {
	tx, err := db.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var version string
	err = tx.QueryRow("SELECT value FROM settings WHERE key = ?", creditUnitVersionKey).Scan(&version)
	if err == nil {
		if version != creditUnitVersion {
			return fmt.Errorf("未知积分单位版本 %q", version)
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	var oldTotal string
	err = tx.QueryRow("SELECT value FROM settings WHERE key = 'stat_credits'").Scan(&oldTotal)
	hasTotal := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var total float64
	if hasTotal {
		total, err = strconv.ParseFloat(oldTotal, 64)
		if err != nil || math.IsNaN(total) || math.IsInf(total, 0) {
			return fmt.Errorf("旧累计积分不是有效金额 %q", oldTotal)
		}
	}

	// 备份仅保留本次需要恢复的计量字段，不复制凭据或对话内容。
	for _, statement := range []string{
		`CREATE TABLE credit_unit_backup_daily AS SELECT date, model_id, credits FROM daily_stats`,
		`CREATE TABLE credit_unit_backup_requests AS SELECT id, credits FROM request_usage`,
		`CREATE TABLE credit_unit_backup_settings AS SELECT key, value FROM settings WHERE key = 'stat_credits'`,
	} {
		if _, err := tx.Exec(statement); err != nil {
			return fmt.Errorf("备份旧积分失败: %w", err)
		}
	}
	for _, statement := range []string{
		"UPDATE daily_stats SET credits = credits / ?",
		"UPDATE request_usage SET credits = credits / ?",
	} {
		if _, err := tx.Exec(statement, billing.CreditUSD); err != nil {
			return fmt.Errorf("转换旧积分失败: %w", err)
		}
	}
	if hasTotal {
		if _, err := tx.Exec("UPDATE settings SET value = ?, updated_at = datetime('now') WHERE key = 'stat_credits'", strconv.FormatFloat(total/billing.CreditUSD, 'g', -1, 64)); err != nil {
			return err
		}
	}
	if _, err := tx.Exec("INSERT INTO settings (key, value) VALUES (?, ?)", creditUnitVersionKey, creditUnitVersion); err != nil {
		return err
	}
	return tx.Commit()
}
