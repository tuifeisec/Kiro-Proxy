package proxy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

const kiroStageVerificationSchema = 1

var errKiroStageVerificationUnsupported = errors.New("Kiro 阶段验证缓存仅支持 Windows")

type kiroStageVerificationIdentity struct {
	Schema        int    `json:"schema"`
	Target        string `json:"target"`
	InstalledHash string `json:"installedHash"`
	AdapterHash   string `json:"adapterHash"`
	Validator     string `json:"validator"`
}

type kiroStageVerificationReceipt struct {
	Schema        int    `json:"schema"`
	Target        string `json:"target"`
	InstalledHash string `json:"installedHash"`
	OriginalHash  string `json:"originalHash"`
	AdapterHash   string `json:"adapterHash"`
	Validator     string `json:"validator"`
}

type kiroStageVerificationEnvelope struct {
	Payload       json.RawMessage `json:"payload"`
	PayloadSHA256 string          `json:"payloadSha256"`
}

type kiroStageVerificationCache struct {
	root      string
	validator string
	protect   func([]byte) ([]byte, error)
	unprotect func([]byte) ([]byte, error)

	mu     sync.RWMutex
	memory map[string]string
}

func newKiroStageVerificationCache(root string, validator string, protect, unprotect func([]byte) ([]byte, error)) *kiroStageVerificationCache {
	return &kiroStageVerificationCache{
		root:      filepath.Clean(root),
		validator: validator,
		protect:   protect,
		unprotect: unprotect,
		memory:    make(map[string]string),
	}
}

var (
	defaultKiroStageVerificationOnce  sync.Once
	defaultKiroStageVerificationValue *kiroStageVerificationCache
	defaultKiroStageVerificationErr   error
)

// defaultKiroStageVerificationCache 仅在首次调用时读取文件，不会在包初始化期间访问文件系统。
func defaultKiroStageVerificationCache() (*kiroStageVerificationCache, error) {
	defaultKiroStageVerificationOnce.Do(func() {
		if runtime.GOOS != "windows" {
			defaultKiroStageVerificationErr = errKiroStageVerificationUnsupported
			return
		}

		protect, unprotect, err := platformKiroStageCacheProtection()
		if err != nil {
			defaultKiroStageVerificationErr = err
			return
		}
		cacheDir, err := os.UserCacheDir()
		if err != nil {
			defaultKiroStageVerificationErr = fmt.Errorf("获取 Kiro 阶段验证缓存目录失败: %w", err)
			return
		}
		executable, err := os.Executable()
		if err != nil {
			defaultKiroStageVerificationErr = fmt.Errorf("获取当前验证器程序路径失败: %w", err)
			return
		}
		executableBytes, err := os.ReadFile(executable)
		if err != nil {
			defaultKiroStageVerificationErr = fmt.Errorf("读取当前验证器程序失败: %w", err)
			return
		}
		validatorHash := sha256.Sum256(executableBytes)
		defaultKiroStageVerificationValue = newKiroStageVerificationCache(
			filepath.Join(cacheDir, "KiroProxy", "stage-verification"),
			hex.EncodeToString(validatorHash[:]),
			protect,
			unprotect,
		)
	})
	return defaultKiroStageVerificationValue, defaultKiroStageVerificationErr
}

func (c *kiroStageVerificationCache) Lookup(target, installedHash, adapterHash string) (originalHash string, hit bool, err error) {
	identity, key, err := c.identity(target, installedHash, adapterHash)
	if err != nil {
		return "", false, err
	}

	c.mu.RLock()
	originalHash, hit = c.memory[key]
	c.mu.RUnlock()
	if hit {
		return originalHash, true, nil
	}

	data, err := readKiroStageCacheFile(c.receiptPath(key))
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("读取 Kiro 阶段验证凭据失败: %w", err)
	}
	receipt, err := c.openReceipt(data)
	if err != nil {
		return "", false, err
	}
	if err := validateKiroStageVerificationReceipt(identity, receipt); err != nil {
		return "", false, err
	}

	c.mu.Lock()
	c.memory[key] = receipt.OriginalHash
	c.mu.Unlock()
	return receipt.OriginalHash, true, nil
}

func (c *kiroStageVerificationCache) Store(target, installedHash, originalHash, adapterHash string) (resultErr error) {
	identity, key, err := c.identity(target, installedHash, adapterHash)
	if err != nil {
		return err
	}
	if !validKiroStageVerificationHash(originalHash) {
		return fmt.Errorf("Kiro 阶段验证凭据中的原版 SHA256 无效: %q", originalHash)
	}

	c.mu.RLock()
	cachedHash, cached := c.memory[key]
	c.mu.RUnlock()
	if cached {
		if cachedHash != originalHash {
			return errKiroStageVerificationConflict
		}
		return nil
	}

	receipt := kiroStageVerificationReceipt{
		Schema:        identity.Schema,
		Target:        identity.Target,
		InstalledHash: identity.InstalledHash,
		OriginalHash:  originalHash,
		AdapterHash:   identity.AdapterHash,
		Validator:     identity.Validator,
	}
	plain, err := encodeKiroStageVerificationReceipt(receipt)
	if err != nil {
		return err
	}
	protected, err := c.protectBytes(plain)
	if err != nil {
		return fmt.Errorf("保护 Kiro 阶段验证凭据失败: %w", err)
	}
	if err := os.MkdirAll(c.root, 0o700); err != nil {
		return fmt.Errorf("创建 Kiro 阶段验证缓存目录失败: %w", err)
	}

	path := c.receiptPath(key)
	temp, err := os.CreateTemp(c.root, ".kiro-stage-verification-*.tmp")
	if err != nil {
		return fmt.Errorf("创建 Kiro 阶段验证凭据临时文件失败: %w", err)
	}
	tempPath := temp.Name()
	defer func() {
		if removeErr := os.Remove(tempPath); removeErr != nil && !os.IsNotExist(removeErr) {
			resultErr = errors.Join(resultErr, fmt.Errorf("清理 Kiro 阶段验证凭据临时文件失败: %w", removeErr))
		}
	}()

	if _, err = io.Copy(temp, bytes.NewReader(protected)); err != nil {
		_ = temp.Close()
		return fmt.Errorf("写入 Kiro 阶段验证凭据临时文件失败: %w", err)
	}
	if err = temp.Sync(); err != nil {
		_ = temp.Close()
		return fmt.Errorf("同步 Kiro 阶段验证凭据临时文件失败: %w", err)
	}
	if err = temp.Close(); err != nil {
		return fmt.Errorf("关闭 Kiro 阶段验证凭据临时文件失败: %w", err)
	}

	if moveErr := atomicKiroStageCachePublish(tempPath, path); moveErr != nil {
		existing, readErr := readKiroStageCacheFile(path)
		if readErr != nil {
			if os.IsNotExist(readErr) {
				return fmt.Errorf("发布 Kiro 阶段验证凭据失败: %w", moveErr)
			}
			return fmt.Errorf("读取已存在的 Kiro 阶段验证凭据失败: %w", readErr)
		}
		existingReceipt, openErr := c.openReceipt(existing)
		if openErr != nil {
			return openErr
		}
		if validateErr := validateKiroStageVerificationReceipt(identity, existingReceipt); validateErr != nil {
			return validateErr
		}
		if existingReceipt.OriginalHash != originalHash {
			return errKiroStageVerificationConflict
		}
	}
	if removeErr := os.Remove(tempPath); removeErr != nil && !os.IsNotExist(removeErr) {
		return fmt.Errorf("清理 Kiro 阶段验证凭据临时文件失败: %w", removeErr)
	}

	c.mu.Lock()
	c.memory[key] = originalHash
	c.mu.Unlock()
	return nil
}

var errKiroStageVerificationConflict = errors.New("同一 Kiro 阶段验证缓存键已存在不同的凭据")

func (c *kiroStageVerificationCache) identity(target, installedHash, adapterHash string) (kiroStageVerificationIdentity, string, error) {
	if c == nil {
		return kiroStageVerificationIdentity{}, "", errors.New("Kiro 阶段验证缓存未初始化")
	}
	if c.root == "" || c.root == "." {
		return kiroStageVerificationIdentity{}, "", errors.New("Kiro 阶段验证缓存目录为空")
	}
	if c.protect == nil || c.unprotect == nil {
		return kiroStageVerificationIdentity{}, "", errors.New("Kiro 阶段验证缓存保护函数未配置")
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{name: "installedHash", value: installedHash},
		{name: "adapterHash", value: adapterHash},
		{name: "validator", value: c.validator},
	} {
		if !validKiroStageVerificationHash(field.value) {
			return kiroStageVerificationIdentity{}, "", fmt.Errorf("Kiro 阶段验证缓存键中的 %s SHA256 无效: %q", field.name, field.value)
		}
	}
	canonicalTarget, err := canonicalKiroStageCacheTarget(target)
	if err != nil {
		return kiroStageVerificationIdentity{}, "", fmt.Errorf("规范化 Kiro 阶段目标路径失败: %w", err)
	}
	identity := kiroStageVerificationIdentity{
		Schema:        kiroStageVerificationSchema,
		Target:        canonicalTarget,
		InstalledHash: installedHash,
		AdapterHash:   adapterHash,
		Validator:     c.validator,
	}
	encoded, err := json.Marshal(identity)
	if err != nil {
		return kiroStageVerificationIdentity{}, "", fmt.Errorf("编码 Kiro 阶段验证缓存键失败: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return identity, hex.EncodeToString(digest[:]), nil
}

func (c *kiroStageVerificationCache) receiptPath(key string) string {
	return filepath.Join(c.root, key+".receipt")
}

func (c *kiroStageVerificationCache) protectBytes(plain []byte) ([]byte, error) {
	if c.protect == nil {
		return nil, errors.New("Kiro 阶段验证凭据保护函数未配置")
	}
	protected, err := c.protect(append([]byte(nil), plain...))
	if err != nil {
		return nil, err
	}
	if len(protected) == 0 {
		return nil, errors.New("Kiro 阶段验证凭据保护函数返回空结果")
	}
	return protected, nil
}

func (c *kiroStageVerificationCache) openReceipt(protected []byte) (kiroStageVerificationReceipt, error) {
	if c.unprotect == nil {
		return kiroStageVerificationReceipt{}, errors.New("Kiro 阶段验证凭据解封函数未配置")
	}
	plain, err := c.unprotect(append([]byte(nil), protected...))
	if err != nil {
		return kiroStageVerificationReceipt{}, fmt.Errorf("解封 Kiro 阶段验证凭据失败: %w", err)
	}
	var envelope kiroStageVerificationEnvelope
	decoder := json.NewDecoder(bytes.NewReader(plain))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return kiroStageVerificationReceipt{}, fmt.Errorf("解析 Kiro 阶段验证凭据包络失败: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return kiroStageVerificationReceipt{}, errors.New("Kiro 阶段验证凭据包络包含多余内容")
		}
		return kiroStageVerificationReceipt{}, fmt.Errorf("检查 Kiro 阶段验证凭据包络尾部失败: %w", err)
	}
	canonicalEnvelope, err := json.Marshal(envelope)
	if err != nil {
		return kiroStageVerificationReceipt{}, fmt.Errorf("编码 Kiro 阶段验证凭据包络失败: %w", err)
	}
	if !bytes.Equal(canonicalEnvelope, plain) {
		return kiroStageVerificationReceipt{}, errors.New("Kiro 阶段验证凭据包络不是规范编码")
	}
	payloadHash := sha256.Sum256(envelope.Payload)
	if !validKiroStageVerificationHash(envelope.PayloadSHA256) || hex.EncodeToString(payloadHash[:]) != envelope.PayloadSHA256 {
		return kiroStageVerificationReceipt{}, errors.New("Kiro 阶段验证凭据 payload SHA256 不匹配")
	}

	var receipt kiroStageVerificationReceipt
	receiptDecoder := json.NewDecoder(bytes.NewReader(envelope.Payload))
	receiptDecoder.DisallowUnknownFields()
	if err := receiptDecoder.Decode(&receipt); err != nil {
		return kiroStageVerificationReceipt{}, fmt.Errorf("解析 Kiro 阶段验证凭据内容失败: %w", err)
	}
	if err := receiptDecoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return kiroStageVerificationReceipt{}, errors.New("Kiro 阶段验证凭据内容包含多余内容")
		}
		return kiroStageVerificationReceipt{}, fmt.Errorf("检查 Kiro 阶段验证凭据内容尾部失败: %w", err)
	}
	canonicalReceipt, err := json.Marshal(receipt)
	if err != nil {
		return kiroStageVerificationReceipt{}, fmt.Errorf("编码 Kiro 阶段验证凭据内容失败: %w", err)
	}
	if !bytes.Equal(canonicalReceipt, envelope.Payload) {
		return kiroStageVerificationReceipt{}, errors.New("Kiro 阶段验证凭据内容不是规范编码")
	}
	return receipt, nil
}

func encodeKiroStageVerificationReceipt(receipt kiroStageVerificationReceipt) ([]byte, error) {
	payload, err := json.Marshal(receipt)
	if err != nil {
		return nil, fmt.Errorf("编码 Kiro 阶段验证凭据内容失败: %w", err)
	}
	payloadHash := sha256.Sum256(payload)
	envelope := kiroStageVerificationEnvelope{
		Payload:       payload,
		PayloadSHA256: hex.EncodeToString(payloadHash[:]),
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("编码 Kiro 阶段验证凭据包络失败: %w", err)
	}
	return encoded, nil
}

func validateKiroStageVerificationReceipt(identity kiroStageVerificationIdentity, receipt kiroStageVerificationReceipt) error {
	if receipt.Schema != identity.Schema ||
		receipt.Target != identity.Target ||
		receipt.InstalledHash != identity.InstalledHash ||
		receipt.AdapterHash != identity.AdapterHash ||
		receipt.Validator != identity.Validator {
		return errors.New("Kiro 阶段验证凭据与当前目标、内容或验证器不匹配")
	}
	if !validKiroStageVerificationHash(receipt.OriginalHash) {
		return fmt.Errorf("Kiro 阶段验证凭据中的原版 SHA256 无效: %q", receipt.OriginalHash)
	}
	return nil
}

func validKiroStageVerificationHash(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func canonicalKiroStageCacheTarget(target string) (string, error) {
	if target == "" {
		return "", errors.New("目标路径为空")
	}
	abs, err := filepath.Abs(target)
	if err != nil {
		return "", err
	}
	canonical := filepath.Clean(abs)
	if runtime.GOOS == "windows" {
		canonical = strings.ToLower(canonical)
	}
	return canonical, nil
}
