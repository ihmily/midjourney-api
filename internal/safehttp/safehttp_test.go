package safehttp

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestValidateHTTPURL(t *testing.T) {
	tests := []struct {
		name    string
		rawURL  string
		wantErr string
	}{
		{
			name:   "https",
			rawURL: "https://example.com/hook",
		},
		{
			name:   "http",
			rawURL: "http://example.com/hook",
		},
		{
			name:    "missing host",
			rawURL:  "/hook",
			wantErr: "valid http(s) URL",
		},
		{
			name:    "unsupported scheme",
			rawURL:  "ftp://example.com/hook",
			wantErr: "http or https",
		},
		{
			name:    "userinfo",
			rawURL:  "https://user:pass@example.com/hook",
			wantErr: "userinfo",
		},
		{
			name:    "empty hostname with port",
			rawURL:  "https://:443/hook",
			wantErr: "hostname",
		},
		{
			name:    "zero port",
			rawURL:  "https://example.com:0/hook",
			wantErr: "valid port",
		},
		{
			name:    "empty explicit port",
			rawURL:  "https://example.com:/hook",
			wantErr: "valid port",
		},
		{
			name:    "negative port",
			rawURL:  "https://example.com:-1/hook",
			wantErr: "valid port",
		},
		{
			name:    "non numeric port",
			rawURL:  "https://example.com:abc/hook?token=secret#fragment",
			wantErr: "valid port",
		},
		{
			name:    "port above range",
			rawURL:  "https://example.com:65536/hook",
			wantErr: "valid port",
		},
		{
			name:    "ipv6 non numeric port",
			rawURL:  "https://[2001:4860:4860::8888]:abc/hook",
			wantErr: "valid port",
		},
		{
			name:   "explicit valid port",
			rawURL: "https://example.com:8443/hook",
		},
		{
			name:   "explicit valid ipv6 port",
			rawURL: "https://[2001:4860:4860::8888]:8443/hook",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateHTTPURL(tt.rawURL, "callback URL")
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateHTTPURL returned error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("ValidateHTTPURL returned nil error, want %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tt.wantErr)
			}
			for _, forbidden := range []string{"token=secret", "#fragment"} {
				if strings.Contains(err.Error(), forbidden) {
					t.Fatalf("error exposed %q: %s", forbidden, err.Error())
				}
			}
		})
	}
}

func TestNormalizeHTTPURLTrimsAndEscapesRequestURL(t *testing.T) {
	got, err := NormalizeHTTPURL("  https://example.com/a file.png?token=secret#fragment  ", "image URL")
	if err != nil {
		t.Fatalf("NormalizeHTTPURL returned error: %v", err)
	}

	if got != "https://example.com/a%20file.png?token=secret#fragment" {
		t.Fatalf("normalized URL = %q", got)
	}
}

func TestValidatePublicHTTPURLRejectsPrivateIPLiteral(t *testing.T) {
	tests := []struct {
		name   string
		rawURL string
	}{
		{
			name:   "ipv4 localhost",
			rawURL: "http://127.0.0.1/hook?token=secret#fragment",
		},
		{
			name:   "ipv4 private",
			rawURL: "https://10.0.0.8/image.png",
		},
		{
			name:   "ipv6 localhost",
			rawURL: "http://[::1]/hook",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidatePublicHTTPURL(tt.rawURL, "callback URL")

			if err == nil {
				t.Fatal("ValidatePublicHTTPURL returned nil error, want private/local rejection")
			}
			if !errors.Is(err, ErrPrivateOrLocalAddress) {
				t.Fatalf("error = %v, want private/local sentinel", err)
			}
			for _, forbidden := range []string{"token=secret", "#fragment"} {
				if strings.Contains(err.Error(), forbidden) {
					t.Fatalf("error exposed %q: %s", forbidden, err.Error())
				}
			}
		})
	}
}

func TestNormalizePublicHTTPURLRejectsPrivateTarget(t *testing.T) {
	_, err := NormalizePublicHTTPURL(" http://127.0.0.1/hook?token=secret#fragment ", "callback URL")

	if err == nil {
		t.Fatal("NormalizePublicHTTPURL returned nil error, want private/local rejection")
	}
	if !errors.Is(err, ErrPrivateOrLocalAddress) {
		t.Fatalf("error = %v, want private/local sentinel", err)
	}
	for _, forbidden := range []string{"token=secret", "#fragment"} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("error exposed %q: %s", forbidden, err.Error())
		}
	}
}

func TestValidatePublicHTTPURLRejectsLocalHostnames(t *testing.T) {
	tests := []struct {
		name   string
		rawURL string
	}{
		{
			name:   "localhost",
			rawURL: "http://localhost/hook?token=secret#fragment",
		},
		{
			name:   "localhost uppercase with trailing dot",
			rawURL: "http://LOCALHOST./hook",
		},
		{
			name:   "localhost subdomain",
			rawURL: "https://api.localhost/hook",
		},
		{
			name:   "mdns local",
			rawURL: "https://printer.local/image.png",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidatePublicHTTPURL(tt.rawURL, "callback URL")

			if err == nil {
				t.Fatal("ValidatePublicHTTPURL returned nil error, want local hostname rejection")
			}
			if !errors.Is(err, ErrPrivateOrLocalAddress) {
				t.Fatalf("error = %v, want private/local sentinel", err)
			}
			for _, forbidden := range []string{"token=secret", "#fragment"} {
				if strings.Contains(err.Error(), forbidden) {
					t.Fatalf("error exposed %q: %s", forbidden, err.Error())
				}
			}
		})
	}
}

func TestValidatePublicHTTPURLAllowsPublicHostAndIP(t *testing.T) {
	for _, rawURL := range []string{
		"https://example.com/hook?token=allowed-here",
		"https://8.8.8.8/image.png",
		"https://[2001:4860:4860::8888]/image.png",
	} {
		t.Run(rawURL, func(t *testing.T) {
			if err := ValidatePublicHTTPURL(rawURL, "callback URL"); err != nil {
				t.Fatalf("ValidatePublicHTTPURL returned error: %v", err)
			}
		})
	}
}

func TestIsPublicIPRejectsNonPublicRanges(t *testing.T) {
	blocked := []string{
		"127.0.0.1",
		"10.0.0.1",
		"172.16.0.1",
		"192.168.1.1",
		"169.254.1.1",
		"100.64.0.1",
		"192.0.2.1",
		"198.18.0.1",
		"198.51.100.1",
		"203.0.113.1",
		"224.0.0.1",
		"240.0.0.1",
		"255.255.255.255",
		"0.0.0.0",
		"::1",
		"64:ff9b::808:808",
		"100::1",
		"2001:db8::1",
		"2002::1",
		"fc00::1",
		"fe80::1",
		"ff02::1",
		"::ffff:127.0.0.1",
	}

	for _, value := range blocked {
		t.Run(value, func(t *testing.T) {
			if IsPublicIP(net.ParseIP(value)) {
				t.Fatalf("%s was treated as public", value)
			}
		})
	}
}

func TestIsPublicIPAcceptsPublicRanges(t *testing.T) {
	for _, value := range []string{
		"8.8.8.8",
		"1.1.1.1",
		"2001:4860:4860::8888",
		"2606:4700:4700::1111",
	} {
		t.Run(value, func(t *testing.T) {
			if !IsPublicIP(net.ParseIP(value)) {
				t.Fatalf("%s was not treated as public", value)
			}
		})
	}
}

func TestResolvePublicIPsRejectsLocalHostnamesBeforeDNS(t *testing.T) {
	_, err := ResolvePublicIPs(context.Background(), "localhost", "callback URL")

	if err == nil {
		t.Fatal("ResolvePublicIPs returned nil error, want local hostname rejection")
	}
	if !errors.Is(err, ErrPrivateOrLocalAddress) {
		t.Fatalf("error = %v, want private/local sentinel", err)
	}
}

func TestPublicClientRejectsPrivateNetworkAddress(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("private-network request unexpectedly reached test server")
	}))
	defer server.Close()

	client := NewPublicClient(time.Second, "test callback", nil)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, server.URL+"/hook", nil)
	if err != nil {
		t.Fatalf("NewRequest returned error: %v", err)
	}

	_, err = client.Do(req)
	if err == nil {
		t.Fatal("expected private network rejection")
	}
	if !IsPrivateOrLocalAddressError(err) {
		t.Fatalf("error = %v, want private/local sentinel", err)
	}
	if !strings.Contains(err.Error(), "private or local address") {
		t.Fatalf("error = %q, want private/local context", err.Error())
	}
}

func TestPublicClientValidatesInitialRequestURL(t *testing.T) {
	tests := []struct {
		name   string
		rawURL string
		want   string
	}{
		{
			name:   "unsupported scheme",
			rawURL: "ftp://example.com/image.png",
			want:   "http or https",
		},
		{
			name:   "userinfo",
			rawURL: "https://user:pass@example.com/hook",
			want:   "userinfo",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewPublicClient(time.Second, "test callback", nil)
			req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, tt.rawURL, nil)
			if err != nil {
				t.Fatalf("NewRequest returned error: %v", err)
			}

			resp, err := client.Do(req)
			if resp != nil {
				_ = resp.Body.Close()
				t.Fatalf("response = %#v, want nil", resp)
			}
			if err == nil {
				t.Fatal("expected URL validation error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tt.want)
			}
		})
	}
}
