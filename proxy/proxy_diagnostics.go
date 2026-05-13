package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"kiro-go/config"
	"kiro-go/outbound"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	proxyDiagnosticSampleSize = 5
	proxyConnectivityURL      = "https://checkip.amazonaws.com/"
	proxyConnectivityTimeout  = 12 * time.Second
	proxyChatTimeout          = 45 * time.Second
	proxyChatPrompt           = "Reply with OK only."
	proxyChatModel            = "claude-sonnet-4.5"
)

func (h *Handler) apiTestProxyConnectivity(w http.ResponseWriter, r *http.Request) {
	proxies := configuredProxyPool()
	if len(proxies) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "Proxy pool is empty"})
		return
	}

	targets := sampleStrings(proxies, proxyDiagnosticSampleSize)
	results := make([]proxyTestResult, len(targets))
	var wg sync.WaitGroup
	for i, proxyURL := range targets {
		wg.Add(1)
		go func(i int, proxyURL string) {
			defer wg.Done()
			results[i] = testProxyConnectivity(proxyURL)
		}(i, proxyURL)
	}
	wg.Wait()

	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":   true,
		"count":     len(results),
		"targetUrl": proxyConnectivityURL,
		"results":   results,
	})
}

func (h *Handler) apiTestProxyChat(w http.ResponseWriter, r *http.Request) {
	proxies := configuredProxyPool()
	if len(proxies) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "Proxy pool is empty"})
		return
	}

	accounts := enabledChatTestAccounts()
	if len(accounts) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "No enabled account with access token"})
		return
	}

	targets := sampleProxyAccountTargets(proxies, accounts, proxyDiagnosticSampleSize)
	results := make([]proxyTestResult, len(targets))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 2)
	for i, target := range targets {
		wg.Add(1)
		go func(i int, target proxyTestTarget) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i] = h.testProxyChat(target)
		}(i, target)
	}
	wg.Wait()

	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"count":   len(results),
		"model":   proxyChatModel,
		"prompt":  proxyChatPrompt,
		"results": results,
	})
}

func configuredProxyPool() []string {
	proxies := config.GetProxyPool()
	if len(proxies) == 0 {
		if normalized, err := outbound.NormalizeProxyLine(config.GetProxyURL()); err == nil && normalized != "" {
			proxies = []string{normalized}
		}
	}
	out := make([]string, 0, len(proxies))
	seen := make(map[string]bool)
	for _, raw := range proxies {
		proxyURL, err := outbound.NormalizeProxyLine(raw)
		if err != nil || proxyURL == "" || seen[proxyURL] {
			continue
		}
		seen[proxyURL] = true
		out = append(out, proxyURL)
	}
	return out
}

func sampleStrings(values []string, limit int) []string {
	if limit <= 0 || limit > len(values) {
		limit = len(values)
	}
	copied := append([]string(nil), values...)
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	rng.Shuffle(len(copied), func(i, j int) {
		copied[i], copied[j] = copied[j], copied[i]
	})
	return copied[:limit]
}

func testProxyConnectivity(proxyURL string) proxyTestResult {
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), proxyConnectivityTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, proxyConnectivityURL, nil)
	if err != nil {
		return proxyDiagnosticResult(proxyURL, "", "", false, 0, "", time.Since(start), err)
	}
	req.Header.Set("User-Agent", "Kiro-Go proxy diagnostics")

	client := &http.Client{
		Timeout:   proxyConnectivityTimeout,
		Transport: outbound.BuildTransport(proxyURL, 10, 2),
	}
	resp, err := client.Do(req)
	if err != nil {
		return proxyDiagnosticResult(proxyURL, "", "", false, 0, "", time.Since(start), err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 128))
	ok := resp.StatusCode >= 200 && resp.StatusCode < 400
	return proxyDiagnosticResult(proxyURL, "", "", ok, resp.StatusCode, strings.TrimSpace(string(body)), time.Since(start), nil)
}

func (h *Handler) testProxyChat(target proxyTestTarget) proxyTestResult {
	start := time.Now()
	account, ok := findAccountByID(target.AccountID)
	if !ok {
		return proxyDiagnosticResult(target.ProxyRaw, target.AccountID, target.Email, false, 0, "", time.Since(start), fmt.Errorf("account not found"))
	}

	payload := ClaudeToKiro(&ClaudeRequest{
		Model:     proxyChatModel,
		MaxTokens: 16,
		Messages:  []ClaudeMessage{{Role: "user", Content: proxyChatPrompt}},
	}, false)
	if account.ProfileArn != "" {
		payload.ProfileArn = account.ProfileArn
	}

	var content strings.Builder
	var inputTokens, outputTokens int
	var endpointName string
	callback := &KiroStreamCallback{
		OnEndpointTry: func(name, _, _ string) {
			if endpointName == "" {
				endpointName = name
			}
		},
		OnText: func(text string, isThinking bool) {
			if !isThinking {
				content.WriteString(text)
			}
		},
		OnComplete: func(inTok, outTok int) {
			inputTokens = inTok
			outputTokens = outTok
		},
		OnError: func(err error) {},
	}

	err := CallKiroAPIWithProxyTimeout(&account, payload, callback, target.ProxyRaw, proxyChatTimeout)
	result := proxyDiagnosticResult(target.ProxyRaw, target.AccountID, target.Email, err == nil, 0, trimDiagnosticSample(content.String()), time.Since(start), err)
	result.Endpoint = endpointName
	if inputTokens > 0 || outputTokens > 0 {
		result.Sample = strings.TrimSpace(fmt.Sprintf("%s tokens in=%d out=%d", result.Sample, inputTokens, outputTokens))
	}
	if err == nil {
		h.pool.RecordSuccess(account.ID)
	} else {
		h.pool.RecordError(account.ID, strings.Contains(err.Error(), "429"))
	}
	return result
}

func proxyDiagnosticResult(proxyURL, accountID, email string, ok bool, statusCode int, sample string, elapsed time.Duration, err error) proxyTestResult {
	result := proxyTestResult{
		ProxyID:    outbound.ProxyID(proxyURL),
		Proxy:      outbound.SafeProxy(proxyURL),
		AccountID:  accountID,
		Email:      email,
		OK:         ok,
		StatusCode: statusCode,
		LatencyMS:  elapsed.Milliseconds(),
		Sample:     trimDiagnosticSample(sample),
	}
	if err != nil {
		result.Error = err.Error()
	}
	return result
}

func trimDiagnosticSample(sample string) string {
	sample = strings.TrimSpace(sample)
	if len(sample) <= 120 {
		return sample
	}
	return sample[:120] + "..."
}

func enabledChatTestAccounts() []config.Account {
	accounts := config.GetAccounts()
	out := make([]config.Account, 0, len(accounts))
	for _, account := range accounts {
		if !account.Enabled || account.AccessToken == "" {
			continue
		}
		if account.BanStatus != "" && account.BanStatus != "ACTIVE" {
			continue
		}
		out = append(out, account)
	}
	return out
}

func findAccountByID(id string) (config.Account, bool) {
	accounts := config.GetAccounts()
	for _, account := range accounts {
		if account.ID == id {
			return account, true
		}
	}
	return config.Account{}, false
}

func sampleProxyAccountTargets(proxies []string, accounts []config.Account, limit int) []proxyTestTarget {
	if len(proxies) == 0 || len(accounts) == 0 {
		return nil
	}

	all := make([]proxyTestTarget, 0, len(proxies)*len(accounts))
	for _, proxyURL := range proxies {
		for _, account := range accounts {
			all = append(all, proxyTestTarget{
				ProxyRaw:  proxyURL,
				ProxyID:   outbound.ProxyID(proxyURL),
				Proxy:     outbound.SafeProxy(proxyURL),
				AccountID: account.ID,
				Email:     account.Email,
			})
		}
	}
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	rng.Shuffle(len(all), func(i, j int) {
		all[i], all[j] = all[j], all[i]
	})

	if limit <= 0 || limit > proxyDiagnosticSampleSize {
		limit = proxyDiagnosticSampleSize
	}
	if limit > len(all) {
		limit = len(all)
	}
	return all[:limit]
}
