package api

import (
	"kiro-proxy/license"
	"net/http"
	"os"
)

func (s *Server) handleLicenseInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	client := license.NewClient(os.Getenv("KIRO_LICENSE_SERVER"))
	machineCode, err := license.MachineCode()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	result, err := client.CheckCached()
	if err != nil {
		writeOK(w, map[string]interface{}{
			"authorized":    false,
			"registered":    false,
			"tampered":      false,
			"version_valid": false,
			"traffic_logs":  false,
			"machine_code":  machineCode,
			"branding": map[string]string{
				"app_name":          "Kiro Proxy",
				"unauthorized_text": "请加入群获取免费授权后使用",
			},
			"error": err.Error(),
		})
		return
	}
	writeOK(w, result)
}

func (s *Server) handleAdvertisements(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ads, err := license.NewClient(os.Getenv("KIRO_LICENSE_SERVER")).Ads()
	if err != nil {
		writeOK(w, []license.Advertisement{})
		return
	}
	writeOK(w, ads)
}
