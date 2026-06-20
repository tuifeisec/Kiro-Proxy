package core

import (
	"errors"
	"strings"
)

func ValidateMachineCode(machineCode string) error {
	if strings.TrimSpace(machineCode) == "" {
		return errors.New("machine_code required")
	}
	return nil
}

func ValidateRelease(r Release) error {
	switch strings.TrimSpace(r.Platform) {
	case "darwin", "windows":
	default:
		return errors.New("platform must be darwin or windows")
	}
	if strings.TrimSpace(r.Version) == "" {
		return errors.New("version required")
	}
	if strings.TrimSpace(r.DownloadURL) == "" {
		return errors.New("download_url required")
	}
	if strings.TrimSpace(r.ExpectedSHA256) == "" {
		return errors.New("expected_sha256 required")
	}
	return nil
}

func ValidateAdvertisement(ad Advertisement) error {
	if strings.TrimSpace(ad.Title) == "" {
		return errors.New("title required")
	}
	return nil
}
