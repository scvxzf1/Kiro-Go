package auth

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	kiroDesktopAuthAPI       = "https://prod.us-east-1.auth.desktop.kiro.dev"
	kiroIDEVersion           = "0.6.18"
	kiroLoginTTL             = 10 * time.Minute
	kiroSocialRedirectURI    = "kiro://kiro.kiroAgent/authenticate-success"
	kiroManualIDCRedirectURI = "http://127.0.0.1/oauth/callback"
)

type KiroLoginSession struct {
	ID           string
	Provider     string
	AuthMethod   string
	State        string
	CodeVerifier string
	RedirectURI  string
	Region       string
	StartURL     string
	ClientID     string
	ClientSecret string
	MachineID    string
	ExpiresAt    time.Time
	Status       string
	Error        string
	Account      *KiroLoginAccount
}

type KiroLoginTokenResult struct {
	SessionID    string
	Provider     string
	AuthMethod   string
	AccessToken  string
	RefreshToken string
	ExpiresIn    int
	ClientID     string
	ClientSecret string
	Region       string
	StartURL     string
	MachineID    string
	ProfileArn   string
	IDToken      string
	TokenType    string
	SSOSessionID string
}

type KiroLoginAccount struct {
	ID     string `json:"id"`
	Email  string `json:"email,omitempty"`
	UserID string `json:"userId,omitempty"`
}

type kiroProviderConfig struct {
	Provider   string
	AuthMethod string
	Region     string
	StartURL   string
}

var (
	kiroLoginSessions = make(map[string]*KiroLoginSession)
	kiroLoginMu       sync.RWMutex
)

func KiroProviderConfig(provider, region, startURL string) (kiroProviderConfig, error) {
	provider = strings.TrimSpace(provider)
	if region == "" {
		region = "us-east-1"
	}

	switch strings.ToLower(provider) {
	case "google":
		return kiroProviderConfig{Provider: "Google", AuthMethod: "social", Region: "us-east-1"}, nil
	case "github":
		return kiroProviderConfig{Provider: "Github", AuthMethod: "social", Region: "us-east-1"}, nil
	case "builderid", "builder-id", "builder_id":
		if startURL == "" {
			startURL = "https://view.awsapps.com/start"
		}
		return kiroProviderConfig{Provider: "BuilderId", AuthMethod: "idc", Region: region, StartURL: startURL}, nil
	case "enterprise":
		if startURL == "" {
			startURL = "https://view.awsapps.com/start"
		}
		return kiroProviderConfig{Provider: "Enterprise", AuthMethod: "idc", Region: region, StartURL: startURL}, nil
	default:
		return kiroProviderConfig{}, fmt.Errorf("unsupported provider: %s", provider)
	}
}

func StartKiroLogin(provider, region, startURL, redirectURI, machineID string) (*KiroLoginSession, string, error) {
	if machineID == "" {
		machineID = GenerateAccountID()
	}

	cfg, err := KiroProviderConfig(provider, region, startURL)
	if err != nil {
		return nil, "", err
	}
	if redirectURI == "" {
		redirectURI = kiroManualIDCRedirectURI
		if cfg.AuthMethod == "social" {
			redirectURI = kiroSocialRedirectURI
		}
	}

	state := GenerateAccountID()
	codeVerifier := generateCodeVerifier()
	codeChallenge := generateCodeChallenge(codeVerifier)

	session := &KiroLoginSession{
		ID:           GenerateAccountID(),
		Provider:     cfg.Provider,
		AuthMethod:   cfg.AuthMethod,
		State:        state,
		CodeVerifier: codeVerifier,
		RedirectURI:  redirectURI,
		Region:       cfg.Region,
		StartURL:     cfg.StartURL,
		MachineID:    machineID,
		ExpiresAt:    time.Now().Add(kiroLoginTTL),
		Status:       "pending",
	}

	var authorizeURL string
	if cfg.AuthMethod == "social" {
		authorizeURL = buildSocialAuthorizeURL(cfg.Provider, redirectURI, codeChallenge, state)
	} else {
		clientID, clientSecret, err := registerAuthCodeClient(cfg.Region, cfg.StartURL, redirectURI)
		if err != nil {
			return nil, "", err
		}
		session.ClientID = clientID
		session.ClientSecret = clientSecret
		authorizeURL = buildIdCAuthorizeURL(cfg.Region, clientID, redirectURI, codeChallenge, state)
	}

	kiroLoginMu.Lock()
	kiroLoginSessions[session.ID] = session
	kiroLoginMu.Unlock()

	go cleanupExpiredKiroLoginSessions()

	return cloneKiroLoginSession(session), authorizeURL, nil
}

func CompleteKiroLogin(state, code, errorParam, errorDescription string) (*KiroLoginTokenResult, error) {
	if errorParam != "" {
		message := fmt.Sprintf("OAuth error: %s", errorParam)
		if errorDescription != "" {
			message = fmt.Sprintf("%s - %s", message, errorDescription)
		}
		markKiroLoginStateFailed(state, message)
		return nil, fmt.Errorf("%s", message)
	}
	if code == "" {
		markKiroLoginStateFailed(state, "missing authorization code")
		return nil, fmt.Errorf("missing authorization code")
	}
	if state == "" {
		return nil, fmt.Errorf("missing state")
	}

	session, err := markKiroLoginProcessing(state)
	if err != nil {
		return nil, err
	}

	var token *KiroLoginTokenResult
	if session.AuthMethod == "social" {
		token, err = exchangeSocialCode(session, code)
	} else {
		token, err = exchangeIdCCode(session, code)
	}
	if err != nil {
		MarkKiroLoginFailed(session.ID, err.Error())
		return nil, err
	}

	return token, nil
}

func CompleteKiroLoginCallback(sessionID, callbackURL string) (*KiroLoginTokenResult, error) {
	session := GetKiroLoginSession(sessionID)
	if session == nil {
		return nil, fmt.Errorf("session not found or expired")
	}

	params, err := parseCallbackURL(callbackURL)
	if err != nil {
		MarkKiroLoginFailed(sessionID, err.Error())
		return nil, err
	}

	state := params.Get("state")
	if state != session.State {
		MarkKiroLoginFailed(sessionID, "state mismatch")
		return nil, fmt.Errorf("state mismatch")
	}

	return CompleteKiroLogin(
		state,
		params.Get("code"),
		params.Get("error"),
		params.Get("error_description"),
	)
}

func MarkKiroLoginCompleted(sessionID string, account KiroLoginAccount) {
	kiroLoginMu.Lock()
	defer kiroLoginMu.Unlock()

	session, ok := kiroLoginSessions[sessionID]
	if !ok {
		return
	}
	session.Status = "completed"
	session.Error = ""
	session.Account = &account
}

func MarkKiroLoginFailed(sessionID, message string) {
	kiroLoginMu.Lock()
	defer kiroLoginMu.Unlock()

	session, ok := kiroLoginSessions[sessionID]
	if !ok {
		return
	}
	session.Status = "failed"
	session.Error = message
}

func GetKiroLoginSession(sessionID string) *KiroLoginSession {
	kiroLoginMu.RLock()
	defer kiroLoginMu.RUnlock()

	session, ok := kiroLoginSessions[sessionID]
	if !ok {
		return nil
	}
	return cloneKiroLoginSession(session)
}

func buildSocialAuthorizeURL(provider, redirectURI, codeChallenge, state string) string {
	params := url.Values{}
	params.Set("idp", provider)
	params.Set("redirect_uri", redirectURI)
	params.Set("code_challenge", codeChallenge)
	params.Set("code_challenge_method", "S256")
	params.Set("state", state)
	return kiroDesktopAuthAPI + "/login?" + params.Encode()
}

func buildIdCAuthorizeURL(region, clientID, redirectURI, codeChallenge, state string) string {
	params := url.Values{}
	params.Set("response_type", "code")
	params.Set("client_id", clientID)
	params.Set("redirect_uri", redirectURI)
	params.Set("scopes", strings.Join(scopes, ","))
	params.Set("state", state)
	params.Set("code_challenge", codeChallenge)
	params.Set("code_challenge_method", "S256")
	return fmt.Sprintf("https://oidc.%s.amazonaws.com/authorize?%s", region, params.Encode())
}

func parseCallbackURL(callbackURL string) (url.Values, error) {
	callbackURL = strings.TrimSpace(callbackURL)
	if callbackURL == "" {
		return nil, fmt.Errorf("callbackUrl is required")
	}

	parsed, err := url.Parse(callbackURL)
	if err == nil && parsed.RawQuery != "" {
		return parsed.Query(), nil
	}

	idx := strings.Index(callbackURL, "?")
	if idx < 0 || idx == len(callbackURL)-1 {
		return nil, fmt.Errorf("callbackUrl must contain query parameters")
	}
	values, parseErr := url.ParseQuery(callbackURL[idx+1:])
	if parseErr != nil {
		return nil, fmt.Errorf("invalid callbackUrl query: %w", parseErr)
	}
	return values, nil
}

func registerAuthCodeClient(region, startURL, redirectURI string) (string, string, error) {
	oidcBase := fmt.Sprintf("https://oidc.%s.amazonaws.com", region)
	payload := map[string]interface{}{
		"clientName":   "Kiro IDE",
		"clientType":   "public",
		"scopes":       scopes,
		"grantTypes":   []string{"authorization_code", "refresh_token"},
		"redirectUris": []string{redirectURI},
		"issuerUrl":    startURL,
	}

	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", oidcBase+"/client/register", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	resp, err := doAuthRequest(req)
	if err != nil {
		return "", "", fmt.Errorf("client registration failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return "", "", fmt.Errorf("client registration failed (%d): %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		ClientID     string `json:"clientId"`
		ClientSecret string `json:"clientSecret"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", "", fmt.Errorf("parse client registration failed: %w", err)
	}
	if result.ClientID == "" || result.ClientSecret == "" {
		return "", "", fmt.Errorf("client registration returned empty client credentials")
	}
	return result.ClientID, result.ClientSecret, nil
}

func exchangeSocialCode(session *KiroLoginSession, code string) (*KiroLoginTokenResult, error) {
	payload := map[string]string{
		"code":          code,
		"code_verifier": session.CodeVerifier,
		"redirect_uri":  session.RedirectURI,
	}

	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", kiroDesktopAuthAPI+"/oauth/token", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", fmt.Sprintf("KiroIDE-%s-%s", kiroIDEVersion, session.MachineID))

	resp, err := doAuthRequest(req)
	if err != nil {
		return nil, fmt.Errorf("social token exchange failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("social token exchange failed (%d): %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		ProfileArn   string `json:"profileArn"`
		ExpiresIn    int    `json:"expiresIn"`
		IDToken      string `json:"idToken"`
		TokenType    string `json:"tokenType"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("parse social token response failed: %w", err)
	}

	return &KiroLoginTokenResult{
		SessionID:    session.ID,
		Provider:     session.Provider,
		AuthMethod:   session.AuthMethod,
		AccessToken:  result.AccessToken,
		RefreshToken: result.RefreshToken,
		ExpiresIn:    result.ExpiresIn,
		Region:       session.Region,
		StartURL:     session.StartURL,
		MachineID:    session.MachineID,
		ProfileArn:   result.ProfileArn,
		IDToken:      result.IDToken,
		TokenType:    result.TokenType,
	}, nil
}

func exchangeIdCCode(session *KiroLoginSession, code string) (*KiroLoginTokenResult, error) {
	oidcBase := fmt.Sprintf("https://oidc.%s.amazonaws.com", session.Region)
	payload := map[string]string{
		"clientId":     session.ClientID,
		"clientSecret": session.ClientSecret,
		"grantType":    "authorization_code",
		"code":         code,
		"codeVerifier": session.CodeVerifier,
		"redirectUri":  session.RedirectURI,
	}

	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", oidcBase+"/token", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	resp, err := doAuthRequest(req)
	if err != nil {
		return nil, fmt.Errorf("IdC token exchange failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("IdC token exchange failed (%d): %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		AccessToken        string `json:"accessToken"`
		RefreshToken       string `json:"refreshToken"`
		ExpiresIn          int    `json:"expiresIn"`
		IDToken            string `json:"idToken"`
		TokenType          string `json:"tokenType"`
		AWSSSOAppSessionID string `json:"aws_sso_app_session_id"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("parse IdC token response failed: %w", err)
	}

	return &KiroLoginTokenResult{
		SessionID:    session.ID,
		Provider:     session.Provider,
		AuthMethod:   session.AuthMethod,
		AccessToken:  result.AccessToken,
		RefreshToken: result.RefreshToken,
		ExpiresIn:    result.ExpiresIn,
		ClientID:     session.ClientID,
		ClientSecret: session.ClientSecret,
		Region:       session.Region,
		StartURL:     session.StartURL,
		MachineID:    session.MachineID,
		IDToken:      result.IDToken,
		TokenType:    result.TokenType,
		SSOSessionID: result.AWSSSOAppSessionID,
	}, nil
}

func markKiroLoginProcessing(state string) (*KiroLoginSession, error) {
	kiroLoginMu.Lock()
	defer kiroLoginMu.Unlock()

	for _, session := range kiroLoginSessions {
		if session.State != state {
			continue
		}
		if time.Now().After(session.ExpiresAt) {
			session.Status = "failed"
			session.Error = "authorization expired"
			return nil, fmt.Errorf("authorization expired")
		}
		if session.Status == "completed" {
			return nil, fmt.Errorf("authorization already completed")
		}
		if session.Status == "processing" {
			return nil, fmt.Errorf("authorization is already processing")
		}
		if session.Status == "failed" {
			return nil, fmt.Errorf("%s", session.Error)
		}
		session.Status = "processing"
		return cloneKiroLoginSession(session), nil
	}

	return nil, fmt.Errorf("session not found or expired")
}

func markKiroLoginStateFailed(state, message string) {
	if state == "" {
		return
	}
	kiroLoginMu.Lock()
	defer kiroLoginMu.Unlock()

	for _, session := range kiroLoginSessions {
		if session.State == state {
			session.Status = "failed"
			session.Error = message
			return
		}
	}
}

func cloneKiroLoginSession(session *KiroLoginSession) *KiroLoginSession {
	if session == nil {
		return nil
	}
	cloned := *session
	if session.Account != nil {
		account := *session.Account
		cloned.Account = &account
	}
	return &cloned
}

func cleanupExpiredKiroLoginSessions() {
	kiroLoginMu.Lock()
	defer kiroLoginMu.Unlock()

	now := time.Now()
	for id, session := range kiroLoginSessions {
		if now.After(session.ExpiresAt) && session.Status != "completed" {
			session.Status = "failed"
			session.Error = "authorization expired"
		}
		if now.After(session.ExpiresAt.Add(30 * time.Minute)) {
			delete(kiroLoginSessions, id)
		}
	}
}
