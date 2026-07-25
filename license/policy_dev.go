//go:build dev

package license

// applyBuildLicensePolicy allows registered machines to exercise local-only
// development builds without weakening release binary validation.
func applyBuildLicensePolicy(result CheckResult) CheckResult {
	result.DevelopmentMode = true
	if !result.Registered {
		return result
	}

	result.Authorized = true
	result.VersionValid = true
	result.Tampered = false
	result.TrafficLogs = true
	return result
}
