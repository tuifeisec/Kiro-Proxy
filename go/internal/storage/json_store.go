package storage

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"kiro-proxy/go/internal/core"
)

const encryptedFilePrefix = "KPENC1:"

var defaultAESKey = sha256.Sum256([]byte("kiro-proxy-license-admin-simple-aes-key-v1"))

type JSONStore struct {
	mu   sync.RWMutex
	path string
	data core.Snapshot
}

func NewJSONStore(path string) (*JSONStore, error) {
	store := &JSONStore{
		path: path,
		data: core.Snapshot{
			Authorizations: map[string]core.Authorization{},
			Releases:       []core.Release{},
			Ads:            []core.Advertisement{},
			Branding:       defaultBranding(),
		},
	}
	if err := store.load(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *JSONStore) Snapshot() core.Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneSnapshot(s.data)
}

func (s *JSONStore) SaveSnapshot(next core.Snapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data = cloneSnapshot(next)
	return s.saveLocked()
}

func (s *JSONStore) load() error {
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s.saveLocked()
	}
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return nil
	}
	plain, err := decryptData(b)
	if err != nil {
		return err
	}
	var data core.Snapshot
	if err := json.Unmarshal(plain, &data); err != nil {
		return err
	}
	if data.Authorizations == nil {
		data.Authorizations = map[string]core.Authorization{}
	}
	if data.Releases == nil {
		data.Releases = []core.Release{}
	}
	for i := range data.Releases {
		data.Releases[i].Platform = normalizeStoredPlatform(data.Releases[i].Platform)
	}
	if data.Ads == nil {
		data.Ads = []core.Advertisement{}
	}
	if data.LoginEvents == nil {
		data.LoginEvents = []core.LoginEvent{}
	}
	if data.ClientLoginEvents == nil {
		data.ClientLoginEvents = []core.ClientLoginEvent{}
	}
	if data.Branding.AppName == "" {
		data.Branding = defaultBranding()
	}
	s.data = data
	return nil
}

func (s *JSONStore) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0755); err != nil {
		return err
	}
	plain, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	encrypted, err := encryptData(plain)
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, encrypted, 0600)
}

func encryptData(plain []byte) ([]byte, error) {
	block, err := aes.NewCipher(defaultAESKey[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	sealed := gcm.Seal(nonce, nonce, plain, nil)
	out := make([]byte, 0, len(encryptedFilePrefix)+base64.StdEncoding.EncodedLen(len(sealed)))
	out = append(out, encryptedFilePrefix...)
	out = base64.StdEncoding.AppendEncode(out, sealed)
	return out, nil
}

func decryptData(raw []byte) ([]byte, error) {
	trimmed := bytes.TrimSpace(raw)
	if !bytes.HasPrefix(trimmed, []byte(encryptedFilePrefix)) {
		return nil, fmt.Errorf("invalid encrypted data prefix")
	}
	encoded := bytes.TrimPrefix(trimmed, []byte(encryptedFilePrefix))
	sealed := make([]byte, base64.StdEncoding.DecodedLen(len(encoded)))
	n, err := base64.StdEncoding.Decode(sealed, encoded)
	if err != nil {
		return nil, err
	}
	sealed = sealed[:n]
	block, err := aes.NewCipher(defaultAESKey[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(sealed) < gcm.NonceSize() {
		return nil, fmt.Errorf("encrypted data too short")
	}
	nonce := sealed[:gcm.NonceSize()]
	ciphertext := sealed[gcm.NonceSize():]
	return gcm.Open(nil, nonce, ciphertext, nil)
}

func cloneSnapshot(in core.Snapshot) core.Snapshot {
	out := core.Snapshot{
		Authorizations:    make(map[string]core.Authorization, len(in.Authorizations)),
		Releases:          append([]core.Release(nil), in.Releases...),
		Ads:               append([]core.Advertisement(nil), in.Ads...),
		Branding:          in.Branding,
		LoginEvents:       append([]core.LoginEvent(nil), in.LoginEvents...),
		ClientLoginEvents: append([]core.ClientLoginEvent(nil), in.ClientLoginEvents...),
	}
	for k, v := range in.Authorizations {
		out.Authorizations[k] = v
	}
	return out
}

func defaultBranding() core.Branding {
	return core.Branding{
		AppName:          "Kiro Proxy",
		UnauthorizedText: "请加入群获取免费授权后使用",
	}
}

func normalizeStoredPlatform(platform string) string {
	platform = strings.TrimSpace(strings.ToLower(platform))
	switch platform {
	case "mac", "macos", "darwin", "":
		return "darwin"
	case "win", "windows":
		return "windows"
	default:
		return platform
	}
}
