package auth

import "testing"

func TestExtractFirstHTTPURL(t *testing.T) {
	text := "xdg-open https://app.kiro.dev/signin?state=abc&redirect_uri=http%3A%2F%2Flocalhost%3A3128\n"
	got := extractFirstHTTPURL(text)
	want := "https://app.kiro.dev/signin?state=abc&redirect_uri=http%3A%2F%2Flocalhost%3A3128"
	if got != want {
		t.Fatalf("extractFirstHTTPURL = %q, want %q", got, want)
	}
}

func TestDetermineKiroCliExportProvider(t *testing.T) {
	var warnings []string
	got := determineKiroCliExportProvider(KiroCliAccount{AuthMethod: "IdC"}, "https://example.awsapps.com/start", &warnings)
	if got != "Enterprise" {
		t.Fatalf("provider = %q, want Enterprise", got)
	}

	warnings = nil
	got = determineKiroCliExportProvider(KiroCliAccount{AuthMethod: "IdC"}, kiroCliBuilderIDStartURL, &warnings)
	if got != "BuilderId" {
		t.Fatalf("provider = %q, want BuilderId", got)
	}

	warnings = nil
	got = determineKiroCliExportProvider(KiroCliAccount{AuthMethod: "social", ProfileArn: "arn:github:test"}, "", &warnings)
	if got != "Github" {
		t.Fatalf("provider = %q, want Github", got)
	}
}

func TestBuildKiroCliExportAccounts(t *testing.T) {
	snapshot := &KiroCliDBSnapshot{
		TokenEntries: []KiroCliAuthEntry{{
			Key: "kirocli:odic:token",
			ParsedToken: &KiroCliTokenData{
				StartURL: "https://example.awsapps.com/start",
			},
		}},
		DeviceRegistration: &KiroCliDeviceRegistration{
			ClientID:     "client-id",
			ClientSecret: "client-secret",
			Region:       "us-east-1",
		},
	}
	accounts, warnings, err := buildKiroCliExportAccounts([]KiroCliAccount{{
		AccessToken:  "access",
		RefreshToken: "refresh",
		Region:       "us-east-1",
		AuthMethod:   "IdC",
		TokenKey:     "kirocli:odic:token",
	}}, snapshot, "6b1b0f61-6653-4ed3-a2e5-73bc7a2288e1")
	if err != nil {
		t.Fatalf("buildKiroCliExportAccounts returned error: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none", warnings)
	}
	if len(accounts) != 1 {
		t.Fatalf("accounts len = %d, want 1", len(accounts))
	}
	if accounts[0].Provider != "Enterprise" {
		t.Fatalf("provider = %q, want Enterprise", accounts[0].Provider)
	}
	if accounts[0].ClientID != "client-id" || accounts[0].ClientSecret != "client-secret" {
		t.Fatalf("client credentials not filled from device registration: %+v", accounts[0])
	}
}
