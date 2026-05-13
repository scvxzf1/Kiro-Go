package outbound

import (
	"net/http"
	"net/url"
	"testing"
	"time"
)

func TestNormalizeProxyPoolTextSupportsReferenceFormats(t *testing.T) {
	got, err := NormalizeProxyPoolText(`
proxy.local:8080
socks5 proxy.local:1080
socks5h//proxy.local:1081
host.local:9000:user:pass
http://proxy.local:8080
`)
	if err != nil {
		t.Fatalf("NormalizeProxyPoolText returned error: %v", err)
	}

	want := []string{
		"http://proxy.local:8080",
		"socks5://proxy.local:1080",
		"socks5h://proxy.local:1081",
		"http://user:pass@host.local:9000",
	}
	if len(got) != len(want) {
		t.Fatalf("expected %d proxies, got %d: %#v", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("proxy[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestProxyBackoffAndRebind(t *testing.T) {
	manager := NewManager()
	manager.Configure("", []string{"http://proxy-a.local:8080", "http://proxy-b.local:8080"})

	first := manager.Acquire("acct-1", KindAuth)
	if first.ProxyURL == "" {
		t.Fatal("expected first proxy")
	}
	for i := 0; i < proxyFailureThreshold; i++ {
		first.RecordFailure()
	}

	status := manager.AccountStatus("acct-1")
	if status.Status != "cooldown" {
		t.Fatalf("expected account binding to be cooling, got %+v", status)
	}

	second := manager.Acquire("acct-1", KindAuth)
	if second.ProxyURL == "" || second.ProxyURL == first.ProxyURL {
		t.Fatalf("expected rebinding to another proxy, first=%q second=%q", first.ProxyURL, second.ProxyURL)
	}
}

func TestRecordSuccessClearsCooldown(t *testing.T) {
	manager := NewManager()
	manager.Configure("", []string{"http://proxy-a.local:8080"})
	lease := manager.Acquire("acct-1", KindAuth)
	for i := 0; i < proxyFailureThreshold; i++ {
		lease.RecordFailure()
	}
	if got := manager.Statuses()[0]; got.Status != "cooldown" {
		t.Fatalf("expected cooldown before success, got %+v", got)
	}

	lease.RecordSuccess()

	if got := manager.Statuses()[0]; got.Status != "ok" || got.ConsecutiveFailure != 0 {
		t.Fatalf("expected success to clear cooldown, got %+v", got)
	}
}

func TestBuildTransportMapsSocks5hToSupportedSocks5(t *testing.T) {
	transport := BuildTransport("socks5h://proxy.local:1080", 1, 1)
	got, err := transport.Proxy(&http.Request{URL: mustParseURL(t, "https://example.com")})
	if err != nil {
		t.Fatalf("unexpected proxy error: %v", err)
	}
	if got.String() != "socks5://proxy.local:1080" {
		t.Fatalf("proxy URL = %q", got.String())
	}
}

func TestLeaseForProxyUsesRequestedProxy(t *testing.T) {
	manager := NewManager()
	manager.Configure("", []string{"http://proxy-a.local:8080", "http://proxy-b.local:8080"})

	lease := manager.LeaseForProxy("acct-1", "http://proxy-b.local:8080", KindAuth)
	if lease == nil {
		t.Fatal("expected lease for configured proxy")
	}
	if lease.ProxyURL != "http://proxy-b.local:8080" {
		t.Fatalf("lease proxy = %q", lease.ProxyURL)
	}
	if got := manager.LeaseForProxy("acct-1", "http://missing.local:8080", KindAuth); got != nil {
		t.Fatalf("expected nil lease for unconfigured proxy, got %+v", got)
	}
}

func TestProxyStatusIncludesStableID(t *testing.T) {
	manager := NewManager()
	manager.Configure("", []string{"http://user:pass@proxy-a.local:8080"})

	status := manager.Statuses()[0]
	if status.ID == "" {
		t.Fatalf("expected proxy id in status: %+v", status)
	}
	if status.Proxy != "http://user:***@proxy-a.local:8080" {
		t.Fatalf("expected safe proxy, got %q", status.Proxy)
	}
}

func TestSafeProxyKeepsSwiftProxySessionUser(t *testing.T) {
	raw := "http://demo_user_custom_zone_CA_sid_36375412_time_10:secret_password@proxy.example.net:7878"
	got := SafeProxy(raw)
	want := "http://demo_user_custom_zone_CA_sid_36375412_time_10:***@proxy.example.net:7878"
	if got != want {
		t.Fatalf("SafeProxy() = %q, want %q", got, want)
	}
}

func TestCooldownIncreasesToCap(t *testing.T) {
	manager := NewManager()
	manager.Configure("", []string{"http://proxy-a.local:8080"})
	lease := manager.Acquire("acct-1", KindAuth)
	for i := 0; i < 10; i++ {
		lease.RecordFailure()
	}
	status := manager.Statuses()[0]
	if status.CooldownLeft <= 0 || time.Duration(status.CooldownLeft)*time.Second > maxProxyCooldown {
		t.Fatalf("cooldown left out of range: %+v", status)
	}
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse URL: %v", err)
	}
	return parsed
}
