package safehttp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const maxRedirects = 10

var ErrPrivateOrLocalAddress = errors.New("host resolves to a private or local address")

type URLValidator func(rawURL string) error

var nonPublicIPPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("255.255.255.255/32"),
	netip.MustParsePrefix("::/128"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("::ffff:0:0/96"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:2::/48"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("ff00::/8"),
}

func NewPublicClient(timeout time.Duration, purpose string, validate URLValidator) *http.Client {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	purpose = strings.TrimSpace(purpose)
	if purpose == "" {
		purpose = "outbound request"
	}
	if validate == nil {
		validate = func(rawURL string) error {
			return ValidatePublicHTTPURL(rawURL, purpose+" URL")
		}
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	dialer := &net.Dialer{Timeout: timeout}

	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("invalid %s address", purpose)
		}

		ips, err := ResolvePublicIPs(ctx, host, purpose)
		if err != nil {
			return nil, err
		}

		var lastErr error
		for _, ip := range ips {
			conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return conn, nil
			}
			lastErr = err
		}
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, fmt.Errorf("%s host did not resolve to an allowed address", purpose)
	}

	return &http.Client{
		Timeout: timeout,
		Transport: validatingTransport{
			base:     transport,
			validate: validate,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return fmt.Errorf("%s stopped after %d redirects", purpose, maxRedirects)
			}
			return validate(req.URL.String())
		},
	}
}

type validatingTransport struct {
	base     http.RoundTripper
	validate URLValidator
}

func (t validatingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req == nil || req.URL == nil {
		return nil, fmt.Errorf("request URL is required")
	}
	if t.validate != nil {
		if err := t.validate(req.URL.String()); err != nil {
			return nil, err
		}
	}

	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}

func ValidateHTTPURL(rawURL, subject string) error {
	_, err := parseHTTPURL(rawURL, subject)
	return err
}

func NormalizeHTTPURL(rawURL, subject string) (string, error) {
	parsed, err := parseHTTPURL(rawURL, subject)
	if err != nil {
		return "", err
	}
	return parsed.String(), nil
}

func ValidatePublicHTTPURL(rawURL, subject string) error {
	parsed, err := parseHTTPURL(rawURL, subject)
	if err != nil {
		return err
	}
	if err := rejectPrivateOrLocalHost(parsed.Hostname(), subject); err != nil {
		return err
	}
	return nil
}

func NormalizePublicHTTPURL(rawURL, subject string) (string, error) {
	parsed, err := parseHTTPURL(rawURL, subject)
	if err != nil {
		return "", err
	}
	if err := rejectPrivateOrLocalHost(parsed.Hostname(), subject); err != nil {
		return "", err
	}
	return parsed.String(), nil
}

func rejectPrivateOrLocalHost(host, subject string) error {
	host = strings.TrimSpace(host)
	if host == "" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil {
		if !IsPublicIP(ip) {
			return fmt.Errorf("%s %w", normalizeURLSubject(subject), ErrPrivateOrLocalAddress)
		}
		return nil
	}
	if isLocalHostname(host) {
		return fmt.Errorf("%s %w", normalizeURLSubject(subject), ErrPrivateOrLocalAddress)
	}
	return nil
}

func isLocalHostname(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	return host == "localhost" ||
		strings.HasSuffix(host, ".localhost") ||
		host == "local" ||
		strings.HasSuffix(host, ".local")
}

func parseHTTPURL(rawURL, subject string) (*url.URL, error) {
	subject = normalizeURLSubject(subject)

	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		if isInvalidPortParseError(err) {
			return nil, fmt.Errorf("%s must use a valid port", subject)
		}
		return nil, fmt.Errorf("%s must be a valid http(s) URL", subject)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("%s must be a valid http(s) URL", subject)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return nil, fmt.Errorf("%s must use http or https", subject)
	}
	if parsed.User != nil {
		return nil, fmt.Errorf("%s must not contain userinfo", subject)
	}
	if strings.TrimSpace(parsed.Hostname()) == "" {
		return nil, fmt.Errorf("%s must include a hostname", subject)
	}
	if err := validateURLPort(parsed, subject); err != nil {
		return nil, err
	}
	return parsed, nil
}

func isInvalidPortParseError(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "invalid port")
}

func validateURLPort(parsed *url.URL, subject string) error {
	if parsed == nil {
		return nil
	}
	port := parsed.Port()
	if port == "" {
		if hasExplicitPort(parsed.Host) {
			return fmt.Errorf("%s must use a valid port", subject)
		}
		return nil
	}
	parsedPort, err := strconv.Atoi(port)
	if err != nil || parsedPort <= 0 || parsedPort > 65535 {
		return fmt.Errorf("%s must use a valid port", subject)
	}
	return nil
}

func hasExplicitPort(host string) bool {
	host = strings.TrimSpace(host)
	if host == "" {
		return false
	}
	if strings.HasPrefix(host, "[") {
		end := strings.LastIndex(host, "]")
		return end >= 0 && len(host) > end+1 && strings.HasPrefix(host[end+1:], ":")
	}
	return strings.Count(host, ":") == 1
}

func normalizeURLSubject(subject string) string {
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return "URL"
	}
	return subject
}

func ResolvePublicIPs(ctx context.Context, host, purpose string) ([]net.IP, error) {
	if err := rejectPrivateOrLocalHost(host, purpose); err != nil {
		return nil, err
	}

	if ip := net.ParseIP(host); ip != nil {
		if !IsPublicIP(ip) {
			return nil, fmt.Errorf("%s %w", purpose, ErrPrivateOrLocalAddress)
		}
		return []net.IP{ip}, nil
	}

	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("%s host did not resolve", purpose)
	}

	ips := make([]net.IP, 0, len(addrs))
	for _, addr := range addrs {
		if !IsPublicIP(addr.IP) {
			return nil, fmt.Errorf("%s %w", purpose, ErrPrivateOrLocalAddress)
		}
		ips = append(ips, addr.IP)
	}
	return ips, nil
}

func IsPrivateOrLocalAddressError(err error) bool {
	return errors.Is(err, ErrPrivateOrLocalAddress)
}

func IsPublicIP(ip net.IP) bool {
	if ip == nil {
		return false
	}

	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	addr = addr.Unmap()
	if !addr.IsValid() || !addr.IsGlobalUnicast() {
		return false
	}

	for _, prefix := range nonPublicIPPrefixes {
		if prefix.Contains(addr) {
			return false
		}
	}
	return true
}
