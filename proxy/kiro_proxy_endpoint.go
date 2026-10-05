package proxy

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

type kiroProxyEndpoint struct {
	scheme string
	host   string
	port   int
}

func parseKiroProxyEndpoint(value string) (kiroProxyEndpoint, bool) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Hostname() == "" || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return kiroProxyEndpoint{}, false
	}
	scheme := strings.ToLower(parsed.Scheme)
	port := 80
	switch scheme {
	case "http":
	case "https":
		port = 443
	default:
		return kiroProxyEndpoint{}, false
	}
	if parsed.Port() != "" {
		port, err = strconv.Atoi(parsed.Port())
		if err != nil || port < 1 || port > 65535 {
			return kiroProxyEndpoint{}, false
		}
	}
	host := strings.ToLower(parsed.Hostname())
	if ip := net.ParseIP(host); ip != nil {
		host = ip.String()
	}
	return kiroProxyEndpoint{scheme: scheme, host: host, port: port}, true
}

func isKiroIPv4Loopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.To4() != nil && ip.IsLoopback()
}

func shouldInjectKiroCA(currentProxy, targetProxy string) bool {
	current, currentOK := parseKiroProxyEndpoint(currentProxy)
	target, targetOK := parseKiroProxyEndpoint(targetProxy)
	return currentOK && targetOK && current.scheme == target.scheme && current.port == target.port &&
		(current.host == target.host || (isKiroLocalhostAlias(current.host) && isKiroLocalhostAlias(target.host)))
}

func isKiroLocalhostAlias(host string) bool {
	return host == "localhost" || host == "127.0.0.1"
}

func kiroCAInjectionDecision(currentProxy, targetProxy string) (bool, error) {
	return kiroCAInjectionDecisionWithAddresses(currentProxy, targetProxy, net.InterfaceAddrs)
}

func kiroCAInjectionDecisionWithAddresses(currentProxy, targetProxy string, addresses func() ([]net.Addr, error)) (bool, error) {
	if shouldInjectKiroCA(currentProxy, targetProxy) {
		return true, nil
	}
	current, currentOK := parseKiroProxyEndpoint(currentProxy)
	target, targetOK := parseKiroProxyEndpoint(targetProxy)
	// 0.0.0.0 comes from the server's LAN bind address, not its changing display address.
	if !currentOK || !targetOK || target.host != "0.0.0.0" || current.scheme != target.scheme || current.port != target.port {
		return false, nil
	}
	if isKiroIPv4Loopback(current.host) {
		return true, nil
	}
	currentIP := net.ParseIP(current.host)
	if currentIP == nil || currentIP.To4() == nil {
		return false, nil
	}
	localAddresses, err := addresses()
	if err != nil {
		return false, fmt.Errorf("读取本机网络地址失败: %w", err)
	}
	for _, address := range localAddresses {
		var localIP net.IP
		switch address := address.(type) {
		case *net.IPNet:
			localIP = address.IP
		case *net.IPAddr:
			localIP = address.IP
		}
		if localIP != nil && currentIP.Equal(localIP) {
			return true, nil
		}
	}
	return false, nil
}
