package core

import "time"

type Authorization struct {
	MachineCode   string    `json:"machine_code"`
	Remark        string    `json:"remark"`
	Enabled       bool      `json:"enabled"`
	TrafficLogs   bool      `json:"traffic_logs"`
	CreatedAt     time.Time `json:"created_at"`
	LastCheckedAt time.Time `json:"last_checked_at"`
}

type LoginEvent struct {
	Username  string    `json:"username"`
	CreatedAt time.Time `json:"created_at"`
}

type ClientLoginEvent struct {
	MachineCode string    `json:"machine_code"`
	CreatedAt   time.Time `json:"created_at"`
}

type Release struct {
	Platform       string    `json:"platform"`
	Version        string    `json:"version"`
	Notes          string    `json:"notes"`
	DownloadURL    string    `json:"download_url"`
	ExpectedSHA256 string    `json:"expected_sha256"`
	CreatedAt      time.Time `json:"created_at"`
}

type Advertisement struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	LinkURL     string    `json:"link_url"`
	ImageURL    string    `json:"image_url"`
	Description string    `json:"description"`
	Enabled     bool      `json:"enabled"`
	CreatedAt   time.Time `json:"created_at"`
}

type LicenseCheckRequest struct {
	MachineCode string `json:"machine_code"`
	Platform    string `json:"platform"`
	Version     string `json:"version"`
	SHA256      string `json:"sha256"`
}

type LicenseCheckResult struct {
	Authorized   bool     `json:"authorized"`
	Registered   bool     `json:"registered"`
	Tampered     bool     `json:"tampered"`
	VersionValid bool     `json:"version_valid"`
	TrafficLogs  bool     `json:"traffic_logs"`
	MachineCode  string   `json:"machine_code"`
	Version      string   `json:"version"`
	Latest       Release  `json:"latest"`
	Branding     Branding `json:"branding"`
}

type Branding struct {
	AppName          string `json:"app_name"`
	UnauthorizedText string `json:"unauthorized_text"`
}

type DashboardDailyStat struct {
	Date           string `json:"date"`
	Logins         int    `json:"logins"`
	Authorizations int    `json:"authorizations"`
}

type DashboardStats struct {
	AuthorizationCount       int                  `json:"authorization_count"`
	RecentLoginCount         int                  `json:"recent_login_count"`
	RecentAuthorizationCount int                  `json:"recent_authorization_count"`
	Daily                    []DashboardDailyStat `json:"daily"`
}

type Snapshot struct {
	Authorizations    map[string]Authorization `json:"authorizations"`
	Releases          []Release                `json:"releases"`
	Ads               []Advertisement          `json:"ads"`
	Branding          Branding                 `json:"branding"`
	LoginEvents       []LoginEvent             `json:"login_events"`
	ClientLoginEvents []ClientLoginEvent       `json:"client_login_events"`
}
