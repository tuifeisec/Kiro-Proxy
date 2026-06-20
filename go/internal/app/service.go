package app

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
	"sync"
	"time"

	"kiro-proxy/go/internal/core"
)

type Store interface {
	Snapshot() core.Snapshot
	SaveSnapshot(core.Snapshot) error
}

type Service struct {
	store    Store
	sessions map[string]time.Time
	mu       sync.RWMutex
}

func NewService(store Store) *Service {
	return &Service{store: store, sessions: map[string]time.Time{}}
}

func (s *Service) Login(username, password string) (string, error) {
	if username != "superadmin" || password != "Aa12123." {
		return "", ErrInvalidCredential
	}
	token, err := randomToken()
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	s.sessions[token] = time.Now().Add(12 * time.Hour)
	s.mu.Unlock()

	snap := s.store.Snapshot()
	snap.LoginEvents = append([]core.LoginEvent{{Username: username, CreatedAt: time.Now()}}, snap.LoginEvents...)
	if len(snap.LoginEvents) > 200 {
		snap.LoginEvents = snap.LoginEvents[:200]
	}
	if err := s.store.SaveSnapshot(snap); err != nil {
		return "", err
	}
	return token, nil
}

func (s *Service) Logout(token string) {
	s.mu.Lock()
	delete(s.sessions, token)
	s.mu.Unlock()
}

func (s *Service) ValidSession(token string) bool {
	s.mu.RLock()
	expiresAt, ok := s.sessions[token]
	s.mu.RUnlock()
	return ok && time.Now().Before(expiresAt)
}

func (s *Service) Snapshot() core.Snapshot {
	return s.store.Snapshot()
}

func (s *Service) Authorize(machineCode, remark string, enabled bool, trafficLogs bool) (core.Authorization, error) {
	machineCode = strings.TrimSpace(machineCode)
	if err := core.ValidateMachineCode(machineCode); err != nil {
		return core.Authorization{}, err
	}
	auth := core.Authorization{MachineCode: machineCode, Remark: strings.TrimSpace(remark), Enabled: enabled, TrafficLogs: trafficLogs, CreatedAt: time.Now()}
	snap := s.store.Snapshot()
	snap.Authorizations[machineCode] = auth
	return auth, s.store.SaveSnapshot(snap)
}

func (s *Service) AddRelease(input core.Release) (core.Release, error) {
	input.Platform = normalizePlatform(input.Platform)
	input.Version = strings.TrimSpace(input.Version)
	input.DownloadURL = strings.TrimSpace(input.DownloadURL)
	input.ExpectedSHA256 = strings.TrimSpace(input.ExpectedSHA256)
	if err := core.ValidateRelease(input); err != nil {
		return core.Release{}, err
	}
	input.CreatedAt = time.Now()
	snap := s.store.Snapshot()
	next := make([]core.Release, 0, len(snap.Releases)+1)
	next = append(next, input)
	for _, item := range snap.Releases {
		if item.Platform == input.Platform && item.Version == input.Version {
			continue
		}
		next = append(next, item)
	}
	snap.Releases = next
	return input, s.store.SaveSnapshot(snap)
}

func (s *Service) DeleteRelease(platform, version string) error {
	platform = normalizePlatform(platform)
	version = strings.TrimSpace(version)
	snap := s.store.Snapshot()
	next := snap.Releases[:0]
	for _, item := range snap.Releases {
		if item.Platform == platform && item.Version == version {
			continue
		}
		next = append(next, item)
	}
	snap.Releases = next
	return s.store.SaveSnapshot(snap)
}

func (s *Service) SaveAdvertisement(input core.Advertisement) (core.Advertisement, error) {
	input.Title = strings.TrimSpace(input.Title)
	if err := core.ValidateAdvertisement(input); err != nil {
		return core.Advertisement{}, err
	}
	if strings.TrimSpace(input.ID) == "" {
		input.ID = time.Now().Format("20060102150405")
	}
	input.CreatedAt = time.Now()
	snap := s.store.Snapshot()
	replaced := false
	for i := range snap.Ads {
		if snap.Ads[i].ID == input.ID {
			snap.Ads[i] = input
			replaced = true
			break
		}
	}
	if !replaced {
		snap.Ads = append([]core.Advertisement{input}, snap.Ads...)
	}
	return input, s.store.SaveSnapshot(snap)
}

func (s *Service) DeleteAdvertisement(id string) error {
	id = strings.TrimSpace(id)
	snap := s.store.Snapshot()
	next := snap.Ads[:0]
	for _, item := range snap.Ads {
		if item.ID != id {
			next = append(next, item)
		}
	}
	snap.Ads = next
	return s.store.SaveSnapshot(snap)
}

func (s *Service) CheckLicense(req core.LicenseCheckRequest) core.LicenseCheckResult {
	snap := s.store.Snapshot()
	machineCode := strings.TrimSpace(req.MachineCode)
	platform := normalizePlatform(req.Platform)
	version := strings.TrimSpace(req.Version)
	auth, registered := snap.Authorizations[machineCode]
	latest := latestRelease(snap.Releases, platform)
	matchedRelease, versionValid := findRelease(snap.Releases, platform, version)
	tampered := versionValid && matchedRelease.ExpectedSHA256 != "" && (req.SHA256 == "" || !strings.EqualFold(matchedRelease.ExpectedSHA256, req.SHA256))
	authorized := registered && auth.Enabled && versionValid && !tampered
	if authorized {
		s.recordClientLogin(machineCode)
	}
	return core.LicenseCheckResult{
		Authorized:   authorized,
		Registered:   registered,
		Tampered:     tampered,
		VersionValid: versionValid,
		TrafficLogs:  authorized && auth.TrafficLogs,
		MachineCode:  req.MachineCode,
		Version:      req.Version,
		Latest:       latest,
		Branding:     snap.Branding,
	}
}

func (s *Service) recordClientLogin(machineCode string) {
	snap := s.store.Snapshot()
	now := time.Now()

	if auth, ok := snap.Authorizations[machineCode]; ok {
		auth.LastCheckedAt = now
		snap.Authorizations[machineCode] = auth
	}

	for _, event := range snap.ClientLoginEvents {
		if event.MachineCode == machineCode {
			if now.Sub(event.CreatedAt) < time.Hour {
				_ = s.store.SaveSnapshot(snap)
				return
			}
			break
		}
	}

	snap.ClientLoginEvents = append([]core.ClientLoginEvent{{MachineCode: machineCode, CreatedAt: now}}, snap.ClientLoginEvents...)
	if len(snap.ClientLoginEvents) > 1000 {
		snap.ClientLoginEvents = snap.ClientLoginEvents[:1000]
	}
	_ = s.store.SaveSnapshot(snap)
}

func (s *Service) EnabledAdvertisements() []core.Advertisement {
	snap := s.store.Snapshot()
	items := make([]core.Advertisement, 0, len(snap.Ads))
	for _, ad := range snap.Ads {
		if ad.Enabled {
			items = append(items, ad)
		}
	}
	return items
}

func (s *Service) LatestRelease(platform string) core.Release {
	return latestRelease(s.store.Snapshot().Releases, platform)
}

func latestRelease(items []core.Release, platform string) core.Release {
	platform = normalizePlatform(platform)
	for _, item := range items {
		if item.Platform == platform {
			return item
		}
	}
	return core.Release{}
}

func findRelease(items []core.Release, platform, version string) (core.Release, bool) {
	platform = normalizePlatform(platform)
	version = strings.TrimSpace(version)
	if version == "" {
		return core.Release{}, false
	}
	for _, item := range items {
		if item.Platform == platform && item.Version == version {
			return item, true
		}
	}
	return core.Release{}, false
}

func normalizePlatform(platform string) string {
	platform = strings.TrimSpace(strings.ToLower(platform))
	switch platform {
	case "mac", "macos", "darwin":
		return "darwin"
	case "win", "windows":
		return "windows"
	default:
		return platform
	}
}

func randomToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func (s *Service) SaveBranding(input core.Branding) (core.Branding, error) {
	input.AppName = strings.TrimSpace(input.AppName)
	input.UnauthorizedText = strings.TrimSpace(input.UnauthorizedText)
	if input.AppName == "" {
		input.AppName = "Kiro Proxy"
	}
	if input.UnauthorizedText == "" {
		input.UnauthorizedText = "请加入群获取免费授权后使用"
	}
	snap := s.store.Snapshot()
	snap.Branding = input
	return input, s.store.SaveSnapshot(snap)
}

func (s *Service) DeleteAuthorization(machineCode string) error {
	machineCode = strings.TrimSpace(machineCode)
	snap := s.store.Snapshot()
	delete(snap.Authorizations, machineCode)
	return s.store.SaveSnapshot(snap)
}

func (s *Service) DashboardStats() core.DashboardStats {
	snap := s.store.Snapshot()
	days := lastSevenDays()
	byDay := make(map[string]*core.DashboardDailyStat, len(days))
	daily := make([]core.DashboardDailyStat, 0, len(days))
	for _, day := range days {
		item := core.DashboardDailyStat{Date: day}
		daily = append(daily, item)
		byDay[day] = &daily[len(daily)-1]
	}

	cutoff := time.Now().AddDate(0, 0, -6)
	cutoff = time.Date(cutoff.Year(), cutoff.Month(), cutoff.Day(), 0, 0, 0, 0, cutoff.Location())
	for _, event := range snap.ClientLoginEvents {
		if event.CreatedAt.Before(cutoff) {
			continue
		}
		day := event.CreatedAt.Format("2006-01-02")
		if item := byDay[day]; item != nil {
			item.Logins++
		}
	}
	for _, auth := range snap.Authorizations {
		if auth.CreatedAt.Before(cutoff) {
			continue
		}
		day := auth.CreatedAt.Format("2006-01-02")
		if item := byDay[day]; item != nil {
			item.Authorizations++
		}
	}

	recentLogins := 0
	recentAuthorizations := 0
	for _, item := range daily {
		recentLogins += item.Logins
		recentAuthorizations += item.Authorizations
	}
	return core.DashboardStats{
		AuthorizationCount:       len(snap.Authorizations),
		RecentLoginCount:         recentLogins,
		RecentAuthorizationCount: recentAuthorizations,
		Daily:                    daily,
	}
}

func lastSevenDays() []string {
	today := time.Now()
	days := make([]string, 0, 7)
	for i := 6; i >= 0; i-- {
		days = append(days, today.AddDate(0, 0, -i).Format("2006-01-02"))
	}
	return days
}
