package proxy

import (
	"errors"
	"io"
	"net/http"
	"strings"

	"kiro-proxy/router"
)

func isKiroActivityRequest(req *http.Request) bool {
	if req == nil || req.URL == nil || req.Method != http.MethodPost || req.URL.Path != "/agents/activity" {
		return false
	}

	host := req.URL.Hostname()
	if host == "" {
		host = req.Host
	}
	return router.DetectProtocol(strings.ToLower(host), "/v1/responses") == "kiro-runtime-responses"
}

// discardKiroActivityRequestBody parses and drains the activity body locally.
// Parsed entries are committed only when parsing, draining, and close all succeed.
func (s *Server) discardKiroActivityRequestBody(req *http.Request) (kiroActivityParseReport, error, error) {
	if req == nil || req.Body == nil {
		report := kiroActivityParseReport{}
		s.logKiroActivityBatch(report, errKiroActivityInvalidJSON, nil)
		return report, errKiroActivityInvalidJSON, nil
	}
	report, parseErr := parseKiroActivityRouting(req.Body)
	_, drainErr := io.Copy(io.Discard, req.Body)
	closeErr := req.Body.Close()
	var parseReadErr *kiroActivityBodyReadError
	if errors.As(parseErr, &parseReadErr) {
		drainErr = errors.Join(parseReadErr, drainErr)
	}
	if parseErr == nil && drainErr == nil && closeErr == nil {
		for _, entry := range report.Entries {
			if s.recordKiroActivityRouting(entry) {
				report.Committed++
			} else {
				report.StoreIgnored++
			}
		}
	}
	consumeErr := errors.Join(drainErr, closeErr)
	s.logKiroActivityBatch(report, parseErr, consumeErr)
	return report, parseErr, consumeErr
}

func newKiroActivityResponse(req *http.Request) *http.Response {
	return &http.Response{
		Status:        "204 No Content",
		StatusCode:    http.StatusNoContent,
		Proto:         req.Proto,
		ProtoMajor:    req.ProtoMajor,
		ProtoMinor:    req.ProtoMinor,
		Header:        make(http.Header),
		Body:          http.NoBody,
		ContentLength: 0,
		Request:       req,
	}
}
