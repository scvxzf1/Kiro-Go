package auth

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

func TestKiroProviderConfig(t *testing.T) {
	tests := []struct {
		name       string
		provider   string
		wantMethod string
		wantID     string
	}{
		{name: "google social", provider: "Google", wantMethod: "social", wantID: "Google"},
		{name: "github social", provider: "Github", wantMethod: "social", wantID: "Github"},
		{name: "builder idc", provider: "BuilderId", wantMethod: "idc", wantID: "BuilderId"},
		{name: "enterprise idc", provider: "Enterprise", wantMethod: "idc", wantID: "Enterprise"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := KiroProviderConfig(tt.provider, "", "")
			if err != nil {
				t.Fatalf("KiroProviderConfig returned error: %v", err)
			}
			if got.AuthMethod != tt.wantMethod {
				t.Fatalf("AuthMethod = %q, want %q", got.AuthMethod, tt.wantMethod)
			}
			if got.Provider != tt.wantID {
				t.Fatalf("Provider = %q, want %q", got.Provider, tt.wantID)
			}
			if got.Region == "" {
				t.Fatal("Region should be defaulted")
			}
		})
	}
}

func TestKiroProviderConfigRejectsUnsupportedProvider(t *testing.T) {
	if _, err := KiroProviderConfig("Unknown", "", ""); err == nil {
		t.Fatal("expected unsupported provider error")
	}
}

func TestStartKiroLoginUsesKiroDeepLinkForSocial(t *testing.T) {
	session, authorizeURL, err := StartKiroLogin("Google", "", "", "", "machine-id")
	if err != nil {
		t.Fatalf("StartKiroLogin returned error: %v", err)
	}
	if session.RedirectURI != kiroSocialRedirectURI {
		t.Fatalf("RedirectURI = %q, want %q", session.RedirectURI, kiroSocialRedirectURI)
	}

	parsed, err := url.Parse(authorizeURL)
	if err != nil {
		t.Fatalf("authorize URL should parse: %v", err)
	}
	if got := parsed.Query().Get("redirect_uri"); got != kiroSocialRedirectURI {
		t.Fatalf("redirect_uri = %q, want %q", got, kiroSocialRedirectURI)
	}
}

func TestParseCallbackURLSupportsKiroDeepLink(t *testing.T) {
	values, err := parseCallbackURL("kiro://kiro.kiroAgent/authenticate-success?code=abc%2Fdef&state=state-1")
	if err != nil {
		t.Fatalf("parseCallbackURL returned error: %v", err)
	}
	if values.Get("code") != "abc/def" {
		t.Fatalf("code = %q, want abc/def", values.Get("code"))
	}
	if values.Get("state") != "state-1" {
		t.Fatalf("state = %q, want state-1", values.Get("state"))
	}
}

func TestGenerateCodeChallengeIsBase64URL(t *testing.T) {
	verifier := generateCodeVerifier()
	if verifier == "" {
		t.Fatal("verifier should not be empty")
	}

	challenge := generateCodeChallenge(verifier)
	if len(challenge) < 40 {
		t.Fatalf("challenge too short: %q", challenge)
	}
	if strings.ContainsAny(challenge, "+/=") {
		t.Fatalf("challenge should be raw base64url, got %q", challenge)
	}
}

func TestSocialTokenShape(t *testing.T) {
	var result struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		ProfileArn   string `json:"profileArn"`
		ExpiresIn    int    `json:"expiresIn"`
		IDToken      string `json:"idToken"`
		TokenType    string `json:"tokenType"`
	}

	body := []byte(`{"accessToken":"access","refreshToken":"refresh","profileArn":"arn:test","expiresIn":3600,"idToken":"id","tokenType":"Bearer"}`)
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("unmarshal social token: %v", err)
	}
	if result.AccessToken != "access" || result.RefreshToken != "refresh" || result.ProfileArn != "arn:test" {
		t.Fatalf("unexpected social token result: %+v", result)
	}
}

func TestIdCTokenShape(t *testing.T) {
	var result struct {
		AccessToken        string `json:"accessToken"`
		RefreshToken       string `json:"refreshToken"`
		ExpiresIn          int    `json:"expiresIn"`
		IDToken            string `json:"idToken"`
		TokenType          string `json:"tokenType"`
		AWSSSOAppSessionID string `json:"aws_sso_app_session_id"`
	}

	body := []byte(`{"accessToken":"access","refreshToken":"refresh","expiresIn":3600,"idToken":"id","tokenType":"Bearer","aws_sso_app_session_id":"session"}`)
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("unmarshal IdC token: %v", err)
	}
	if result.AWSSSOAppSessionID != "session" {
		t.Fatalf("aws_sso_app_session_id = %q, want session", result.AWSSSOAppSessionID)
	}
}
