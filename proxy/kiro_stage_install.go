package proxy

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

//go:embed kiro_stage_bridge_manifest.json
var kiroStageBridgeManifest embed.FS

type kiroStagePatch struct {
	Name   string `json:"name"`
	Before string `json:"before"`
	After  string `json:"after"`
}

type kiroStageManifest struct {
	OriginalSHA256   string                     `json:"original_sha256"`
	CandidateSHA256  string                     `json:"candidate_sha256"`
	Patches          []kiroStagePatch           `json:"patches"`
	PreviousVersions []kiroStagePreviousVersion `json:"previous_versions,omitempty"`
}

type kiroStagePreviousVersion struct {
	CandidateSHA256 string           `json:"candidate_sha256"`
	Patches         []kiroStagePatch `json:"patches"`
}

type kiroStageInstallPlan struct {
	target        string
	expectedHash  string
	originalHash  string
	candidateHash string
	candidate     []byte
}

type kiroStageInstallResult struct {
	backupPath string
}

func kiroStageBundlePath(executable string) string {
	return filepath.Join(filepath.Dir(executable), "resources", "app", "extensions", "kiro.kiro-agent", "dist", "extension.js")
}

func readKiroStageManifest() (kiroStageManifest, error) {
	data, err := kiroStageBridgeManifest.ReadFile("kiro_stage_bridge_manifest.json")
	if err != nil {
		return kiroStageManifest{}, fmt.Errorf("读取内嵌 Kiro 阶段桥接清单失败: %w", err)
	}
	var manifest kiroStageManifest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return kiroStageManifest{}, fmt.Errorf("解析内嵌 Kiro 阶段桥接清单失败: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return kiroStageManifest{}, fmt.Errorf("内嵌 Kiro 阶段桥接清单存在多余内容: %v", err)
	}
	if !validKiroStageHash(manifest.OriginalSHA256) || !validKiroStageHash(manifest.CandidateSHA256) || manifest.OriginalSHA256 == manifest.CandidateSHA256 {
		return kiroStageManifest{}, fmt.Errorf("内嵌 Kiro 阶段桥接清单的 SHA256 无效")
	}
	if err := validateKiroStagePatches(manifest.Patches); err != nil {
		return kiroStageManifest{}, err
	}
	seenHashes := map[string]bool{manifest.OriginalSHA256: true, manifest.CandidateSHA256: true}
	for _, previous := range manifest.PreviousVersions {
		if !validKiroStageHash(previous.CandidateSHA256) || seenHashes[previous.CandidateSHA256] {
			return kiroStageManifest{}, fmt.Errorf("内嵌 Kiro 旧桥接版本 SHA256 无效或重复")
		}
		seenHashes[previous.CandidateSHA256] = true
		if err := validateKiroStagePatches(previous.Patches); err != nil {
			return kiroStageManifest{}, err
		}
	}
	return manifest, nil
}

func validateKiroStagePatches(patches []kiroStagePatch) error {
	if len(patches) == 0 {
		return fmt.Errorf("内嵌 Kiro 阶段桥接清单没有补丁")
	}
	seen := make(map[string]bool, len(patches))
	for _, patch := range patches {
		if patch.Name == "" || seen[patch.Name] || patch.Before == "" || patch.After == "" || patch.Before == patch.After {
			return fmt.Errorf("内嵌 Kiro 阶段桥接清单补丁无效: %q", patch.Name)
		}
		seen[patch.Name] = true
	}
	return nil
}

func validKiroStageHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func kiroStageSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func readKiroStageBundle(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("读取 Kiro 阶段桥接目标状态失败: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("Kiro 阶段桥接目标不是普通文件: %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取 Kiro 阶段桥接目标失败: %w", err)
	}
	return data, nil
}

func buildKiroStageCandidate(original []byte, manifest kiroStageManifest) ([]byte, error) {
	if got := kiroStageSHA256(original); got != manifest.OriginalSHA256 {
		return nil, fmt.Errorf("Kiro 阶段桥接原文件 SHA256 不匹配: %s", got)
	}
	candidate := append([]byte(nil), original...)
	for _, patch := range manifest.Patches {
		before := []byte(patch.Before)
		if count := bytes.Count(candidate, before); count != 1 {
			return nil, fmt.Errorf("Kiro 阶段桥接锚点 %q 不是唯一匹配: %d", patch.Name, count)
		}
		candidate = bytes.Replace(candidate, before, []byte(patch.After), 1)
	}
	if got := kiroStageSHA256(candidate); got != manifest.CandidateSHA256 {
		return nil, fmt.Errorf("Kiro 阶段桥接候选 SHA256 不匹配: %s", got)
	}
	return candidate, nil
}

// Upgrade only an exact known bundle, reconstructing and verifying the original
// entirely in memory before applying the new manifest. The installed old bridge
// remains the backup target; this never writes an intermediate original file.
func buildKiroStageUpgrade(current []byte, manifest kiroStageManifest) ([]byte, error) {
	currentHash := kiroStageSHA256(current)
	if currentHash == manifest.CandidateSHA256 {
		return append([]byte(nil), current...), nil
	}
	if currentHash == manifest.OriginalSHA256 {
		return buildKiroStageCandidate(current, manifest)
	}
	for _, previous := range manifest.PreviousVersions {
		if currentHash != previous.CandidateSHA256 {
			continue
		}
		original := append([]byte(nil), current...)
		for index := len(previous.Patches) - 1; index >= 0; index-- {
			patch := previous.Patches[index]
			after := []byte(patch.After)
			if count := bytes.Count(original, after); count != 1 {
				return nil, fmt.Errorf("Kiro 旧桥接逆向锚点 %q 不是唯一匹配: %d", patch.Name, count)
			}
			original = bytes.Replace(original, after, []byte(patch.Before), 1)
		}
		return buildKiroStageCandidate(original, manifest)
	}
	return nil, fmt.Errorf("不支持的 Kiro 阶段桥接目标 SHA256: %s；未停止或修改 Kiro", currentHash)
}

func prepareKiroStageInstall(executable string) (*kiroStageInstallPlan, error) {
	manifest, err := readKiroStageManifest()
	if err != nil {
		return nil, err
	}
	target := kiroStageBundlePath(executable)
	current, err := readKiroStageBundle(target)
	if err != nil {
		return nil, err
	}
	currentHash := kiroStageSHA256(current)
	plan := &kiroStageInstallPlan{
		target:        target,
		expectedHash:  currentHash,
		originalHash:  manifest.OriginalSHA256,
		candidateHash: manifest.CandidateSHA256,
	}
	if currentHash == manifest.CandidateSHA256 {
		return plan, nil
	}
	plan.candidate, err = buildKiroStageUpgrade(current, manifest)
	if err != nil {
		return nil, err
	}
	return plan, nil
}

func installKiroStageBridge(plan *kiroStageInstallPlan) (kiroStageInstallResult, error) {
	if plan == nil || plan.target == "" || !validKiroStageHash(plan.expectedHash) || !validKiroStageHash(plan.candidateHash) {
		return kiroStageInstallResult{}, fmt.Errorf("Kiro 阶段桥接安装计划无效")
	}
	current, err := readKiroStageBundle(plan.target)
	if err != nil {
		return kiroStageInstallResult{}, err
	}
	if got := kiroStageSHA256(current); got != plan.expectedHash {
		return kiroStageInstallResult{}, fmt.Errorf("Kiro 阶段桥接目标在预检后发生变化: 预期 %s，实际 %s", plan.expectedHash, got)
	}
	if plan.expectedHash == plan.candidateHash {
		return kiroStageInstallResult{}, nil
	}
	if !validKiroStageHash(plan.originalHash) || len(plan.candidate) == 0 || kiroStageSHA256(plan.candidate) != plan.candidateHash {
		return kiroStageInstallResult{}, fmt.Errorf("Kiro 阶段桥接安装计划候选无效")
	}

	directory := filepath.Dir(plan.target)
	backupFile, err := os.CreateTemp(directory, filepath.Base(plan.target)+".kiro-proxy-stage-backup-*")
	if err != nil {
		return kiroStageInstallResult{}, fmt.Errorf("创建 Kiro 原文件唯一备份失败: %w", err)
	}
	backupPath := backupFile.Name()
	if err := writeSyncedKiroStageFile(backupFile, current); err != nil {
		return kiroStageInstallResult{}, fmt.Errorf("写入 Kiro 原文件备份 %s 失败: %w", backupPath, err)
	}
	backupBytes, err := os.ReadFile(backupPath)
	if err != nil {
		return kiroStageInstallResult{}, fmt.Errorf("验证 Kiro 原文件备份 %s 失败: %w", backupPath, err)
	}
	if got := kiroStageSHA256(backupBytes); got != plan.expectedHash {
		return kiroStageInstallResult{}, fmt.Errorf("Kiro 原文件备份 %s SHA256 不匹配: %s", backupPath, got)
	}

	candidateFile, err := os.CreateTemp(directory, filepath.Base(plan.target)+".kiro-proxy-stage-candidate-*")
	if err != nil {
		return kiroStageInstallResult{}, fmt.Errorf("创建 Kiro 桥接临时文件失败；原文件备份: %s: %w", backupPath, err)
	}
	candidatePath := candidateFile.Name()
	defer os.Remove(candidatePath)
	if err := writeSyncedKiroStageFile(candidateFile, plan.candidate); err != nil {
		return kiroStageInstallResult{}, fmt.Errorf("写入 Kiro 桥接临时文件失败；原文件备份: %s: %w", backupPath, err)
	}
	candidateBytes, err := os.ReadFile(candidatePath)
	if err != nil {
		return kiroStageInstallResult{}, fmt.Errorf("验证 Kiro 桥接临时文件失败；原文件备份: %s: %w", backupPath, err)
	}
	if got := kiroStageSHA256(candidateBytes); got != plan.candidateHash {
		return kiroStageInstallResult{}, fmt.Errorf("Kiro 桥接临时文件 SHA256 不匹配: %s；原文件备份: %s", got, backupPath)
	}
	latest, err := readKiroStageBundle(plan.target)
	if err != nil {
		return kiroStageInstallResult{}, fmt.Errorf("替换前核对 Kiro 原文件失败；原文件备份: %s: %w", backupPath, err)
	}
	if got := kiroStageSHA256(latest); got != plan.expectedHash {
		return kiroStageInstallResult{}, fmt.Errorf("Kiro 原文件在备份后发生变化: %s；原文件备份: %s", got, backupPath)
	}
	if err := replaceKiroStageFile(candidatePath, plan.target); err != nil {
		return kiroStageInstallResult{}, fmt.Errorf("原子替换 Kiro 阶段桥接失败；原文件备份: %s: %w", backupPath, err)
	}
	installed, err := readKiroStageBundle(plan.target)
	if err != nil {
		return kiroStageInstallResult{}, fmt.Errorf("替换后读取 Kiro 阶段桥接失败；原文件备份: %s: %w", backupPath, err)
	}
	if got := kiroStageSHA256(installed); got != plan.candidateHash {
		return kiroStageInstallResult{}, fmt.Errorf("替换后 Kiro 阶段桥接 SHA256 不匹配: %s；原文件备份: %s", got, backupPath)
	}
	return kiroStageInstallResult{backupPath: backupPath}, nil
}

func writeSyncedKiroStageFile(file *os.File, data []byte) error {
	written, writeErr := file.Write(data)
	if writeErr == nil && written != len(data) {
		writeErr = io.ErrShortWrite
	}
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}
