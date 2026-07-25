//go:build !dev

package license

func applyBuildLicensePolicy(result CheckResult) CheckResult {
	return result
}
