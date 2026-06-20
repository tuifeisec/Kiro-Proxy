package license

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"
)

const DefaultServerURL = "https://kiro.bianliquan.com"

var AppVersion = "dev"

var checkCache = struct {
	sync.Mutex
	expiresAt time.Time
	result    CheckResult
	err       error
}{}

type Client struct {
	ServerURL string
	HTTP      *http.Client
}

type CheckResult struct {
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

type Release struct {
	Platform       string `json:"platform"`
	Version        string `json:"version"`
	Notes          string `json:"notes"`
	DownloadURL    string `json:"download_url"`
	ExpectedSHA256 string `json:"expected_sha256"`
	CreatedAt      string `json:"created_at"`
}

type Advertisement struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	LinkURL     string `json:"link_url"`
	ImageURL    string `json:"image_url"`
	Description string `json:"description"`
	Enabled     bool   `json:"enabled"`
	CreatedAt   string `json:"created_at"`
}

func NewClient(serverURL string) *Client {
	serverURL = strings.TrimRight(strings.TrimSpace(serverURL), "/")
	if serverURL == "" {
		serverURL = DefaultServerURL
	}
	return &Client{ServerURL: serverURL, HTTP: &http.Client{Timeout: 5 * time.Second}}
}

func (c *Client) Check() (CheckResult, error) {
	return c.check(false)
}

func (c *Client) CheckCached() (CheckResult, error) {
	return c.check(true)
}

func (c *Client) check(useCache bool) (CheckResult, error) {
	if !useCache {
		return c.checkRemote()
	}

	checkCache.Lock()
	defer checkCache.Unlock()

	now := time.Now()
	if now.Before(checkCache.expiresAt) {
		return checkCache.result, checkCache.err
	}

	result, err := c.checkRemote()
	checkCache.result = result
	checkCache.err = err
	checkCache.expiresAt = nextCheckExpiresAt(now)
	return result, err
}

func nextCheckExpiresAt(now time.Time) time.Time {
	jitterMinutes := int64(20)
	value, err := rand.Int(rand.Reader, big.NewInt(jitterMinutes+1))
	if err != nil {
		return now.Add(time.Hour)
	}
	return now.Add(time.Hour + time.Duration(value.Int64())*time.Minute)
}

func (c *Client) checkRemote() (CheckResult, error) {
	machineCode, err := MachineCode()
	if err != nil {
		return CheckResult{}, err
	}

	payload := map[string]string{"machine_code": machineCode, "platform": runtime.GOOS, "version": AppVersion, "sha256": ExecutableSHA256()}
	var out CheckResult
	if err := c.postJSON("/api/license/check", payload, &out); err != nil {
		return CheckResult{}, err
	}
	return out, nil
}

func (c *Client) Ads() ([]Advertisement, error) {
	var out []Advertisement
	if err := c.getJSON("/api/ads", &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) LatestRelease() (Release, error) {
	var out Release
	if err := c.getJSON("/api/releases/latest?platform="+runtime.GOOS, &out); err != nil {
		return Release{}, err
	}
	return out, nil
}

func (c *Client) postJSON(path string, payload interface{}, out interface{}) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	resp, err := c.HTTP.Post(c.ServerURL+path, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return errors.New(resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) getJSON(path string, out interface{}) error {
	resp, err := c.HTTP.Get(c.ServerURL + path)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return errors.New(resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func ExecutableSHA256() string {
	path, err := os.Executable()
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func platformTag() string {
	return runtime.GOOS + "/" + runtime.GOARCH
}
