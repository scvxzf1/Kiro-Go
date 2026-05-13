package proxy

import (
	"kiro-go/config"
	"testing"
)

func TestSampleProxyAccountTargetsKeepsTraceability(t *testing.T) {
	proxies := []string{
		"http://user:pass@proxy-a.local:8080",
		"http://proxy-b.local:8080",
	}
	accounts := []config.Account{
		{ID: "acct-1", Email: "one@example.com"},
		{ID: "acct-2", Email: "two@example.com"},
	}

	targets := sampleProxyAccountTargets(proxies, accounts, 5)
	if len(targets) != 4 {
		t.Fatalf("expected all proxy/account combinations when total < 5, got %d", len(targets))
	}
	for _, target := range targets {
		if target.ProxyID == "" || target.Proxy == "" || target.ProxyRaw == "" {
			t.Fatalf("expected proxy trace fields: %+v", target)
		}
		if target.AccountID == "" || target.Email == "" {
			t.Fatalf("expected account trace fields: %+v", target)
		}
		if target.Proxy == target.ProxyRaw && target.ProxyRaw == "http://user:pass@proxy-a.local:8080" {
			t.Fatalf("expected sensitive proxy credentials to be masked: %+v", target)
		}
	}
}

func TestProxyDiagnosticResultMasksProxy(t *testing.T) {
	result := proxyDiagnosticResult("http://user:pass@proxy.local:8080", "acct-1", "user@example.com", true, 200, "ok", 0, nil)
	if result.ProxyID == "" {
		t.Fatal("expected proxy id")
	}
	if result.Proxy != "http://***@proxy.local:8080" {
		t.Fatalf("expected masked proxy, got %q", result.Proxy)
	}
	if result.AccountID != "acct-1" || result.Email != "user@example.com" {
		t.Fatalf("expected account trace fields, got %+v", result)
	}
}

func TestTrimDiagnosticSample(t *testing.T) {
	long := ""
	for i := 0; i < 130; i++ {
		long += "x"
	}
	got := trimDiagnosticSample(long)
	if len(got) != 123 {
		t.Fatalf("expected 120 chars plus ellipsis, got len=%d", len(got))
	}
}
