package outbound

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type ClientKind string

const (
	KindAuth   ClientKind = "auth"
	KindRest   ClientKind = "rest"
	KindStream ClientKind = "stream"
)

const (
	proxyFailureThreshold = 3
	minProxyCooldown      = 60 * time.Second
	maxProxyCooldown      = 600 * time.Second
)

var splitProxyPoolRe = regexp.MustCompile(`[\n\r,]+`)

type Manager struct {
	mu          sync.RWMutex
	proxies     []string
	proxySet    map[string]bool
	bindings    map[string]string
	stats       map[string]*proxyStats
	clients     map[string]*http.Client
	nextIndex   uint64
	configStamp uint64
}

type proxyStats struct {
	Success            int
	Fail               int
	ConsecutiveFailure int
	CooldownUntil      time.Time
	LastFailureAt      time.Time
	LastSuccessAt      time.Time
}

type ClientLease struct {
	Client   *http.Client
	Key      string
	ProxyURL string
	manager  *Manager
}

type ProxyStatus struct {
	ID                 string `json:"id"`
	Proxy              string `json:"proxy"`
	Status             string `json:"status"`
	CooldownLeft       int    `json:"cooldownLeft"`
	Success            int    `json:"success"`
	Fail               int    `json:"fail"`
	ConsecutiveFailure int    `json:"consecutiveFailure"`
	BoundAccounts      int    `json:"boundAccounts"`
}

type AccountProxyStatus struct {
	ID           string `json:"id,omitempty"`
	Proxy        string `json:"proxy,omitempty"`
	Status       string `json:"status"`
	CooldownLeft int    `json:"cooldownLeft,omitempty"`
}

var defaultManager = NewManager()

func DefaultManager() *Manager {
	return defaultManager
}

func Configure(proxyURL string, proxyPool []string) {
	defaultManager.Configure(proxyURL, proxyPool)
}

func Enabled() bool {
	return defaultManager.Enabled()
}

func Acquire(key string, kind ClientKind) *ClientLease {
	return defaultManager.Acquire(key, kind)
}

func LeaseForProxy(key, proxyURL string, kind ClientKind) *ClientLease {
	return defaultManager.LeaseForProxy(key, proxyURL, kind)
}

func Statuses() []ProxyStatus {
	return defaultManager.Statuses()
}

func AccountStatus(key string) AccountProxyStatus {
	return defaultManager.AccountStatus(key)
}

func NormalizeProxyPoolText(raw string) ([]string, error) {
	var out []string
	seen := make(map[string]bool)
	for _, part := range splitProxyPoolRe.Split(raw, -1) {
		proxy, err := NormalizeProxyLine(part)
		if err != nil {
			return nil, err
		}
		if proxy == "" || seen[proxy] {
			continue
		}
		seen[proxy] = true
		out = append(out, proxy)
	}
	return out, nil
}

func NormalizeProxyLine(line string) (string, error) {
	text := strings.TrimSpace(line)
	if text == "" {
		return "", nil
	}

	if hasProxyScheme(text) {
		return validateProxyURL(text)
	}

	lower := strings.ToLower(text)
	if strings.HasPrefix(lower, "socks5h") {
		rest := strings.TrimSpace(text[len("socks5h"):])
		rest = strings.TrimPrefix(rest, "//")
		return validateProxyURL("socks5h://" + strings.TrimSpace(rest))
	}
	if strings.HasPrefix(lower, "socks5") {
		rest := strings.TrimSpace(text[len("socks5"):])
		rest = strings.TrimPrefix(rest, "//")
		return validateProxyURL("socks5://" + strings.TrimSpace(rest))
	}

	parts := strings.SplitN(text, ":", 4)
	if len(parts) == 4 {
		host, port, user, pass := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), parts[2], parts[3]
		if host == "" || port == "" {
			return "", fmt.Errorf("invalid proxy %q: host and port are required", line)
		}
		u := &url.URL{
			Scheme: "http",
			Host:   host + ":" + port,
			User:   url.UserPassword(user, pass),
		}
		return validateProxyURL(u.String())
	}

	return validateProxyURL("http://" + text)
}

func ProxyPoolText(proxies []string) string {
	return strings.Join(proxies, "\n")
}

func SafeProxy(proxyURL string) string {
	if proxyURL == "" {
		return ""
	}
	u, err := url.Parse(proxyURL)
	if err != nil || u.User == nil {
		return proxyURL
	}
	safe := *u
	username := safe.User.Username()
	if username == "" {
		safe.User = url.User("***")
		return safe.String()
	}
	safe.User = nil
	return safe.Scheme + "://" + url.User(username).String() + ":***@" + safe.Host
}

func ProxyID(proxyURL string) string {
	sum := sha256.Sum256([]byte(proxyURL))
	return hex.EncodeToString(sum[:])[:12]
}

func NewManager() *Manager {
	return &Manager{
		proxySet: make(map[string]bool),
		bindings: make(map[string]string),
		stats:    make(map[string]*proxyStats),
		clients:  make(map[string]*http.Client),
	}
}

func (m *Manager) Configure(proxyURL string, proxyPool []string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	next := dedupeProxies(proxyPool)
	if len(next) == 0 {
		if normalized, err := NormalizeProxyLine(proxyURL); err == nil && normalized != "" {
			next = []string{normalized}
		}
	}

	nextSet := make(map[string]bool, len(next))
	nextStats := make(map[string]*proxyStats, len(next))
	for _, proxy := range next {
		nextSet[proxy] = true
		if existing := m.stats[proxy]; existing != nil {
			nextStats[proxy] = existing
		} else {
			nextStats[proxy] = &proxyStats{}
		}
	}

	m.proxies = next
	m.proxySet = nextSet
	m.stats = nextStats
	m.bindings = make(map[string]string)
	m.clients = make(map[string]*http.Client)
	atomic.AddUint64(&m.configStamp, 1)
}

func (m *Manager) Enabled() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.proxies) > 0
}

func (m *Manager) Acquire(key string, kind ClientKind) *ClientLease {
	if strings.TrimSpace(key) == "" {
		key = "default"
	}

	m.mu.Lock()
	proxyURL := m.selectProxyLocked(key)
	client := m.clientLocked(proxyURL, kind)
	m.mu.Unlock()

	return &ClientLease{
		Client:   client,
		Key:      key,
		ProxyURL: proxyURL,
		manager:  m,
	}
}

func (m *Manager) LeaseForProxy(key, proxyURL string, kind ClientKind) *ClientLease {
	if strings.TrimSpace(key) == "" {
		key = "default"
	}
	proxyURL = strings.TrimSpace(proxyURL)
	if proxyURL == "" {
		return nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.proxySet[proxyURL] {
		return nil
	}
	client := m.clientLocked(proxyURL, kind)
	return &ClientLease{
		Client:   client,
		Key:      key,
		ProxyURL: proxyURL,
		manager:  m,
	}
}

func (l *ClientLease) RecordSuccess() {
	if l == nil || l.manager == nil || l.ProxyURL == "" {
		return
	}
	l.manager.RecordSuccess(l.ProxyURL)
}

func (l *ClientLease) RecordFailure() {
	if l == nil || l.manager == nil || l.ProxyURL == "" {
		return
	}
	l.manager.RecordFailure(l.ProxyURL)
}

func (m *Manager) RecordSuccess(proxyURL string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.ensureStatsLocked(proxyURL)
	if st == nil {
		return
	}
	st.Success++
	st.ConsecutiveFailure = 0
	st.CooldownUntil = time.Time{}
	st.LastSuccessAt = time.Now()
}

func (m *Manager) RecordFailure(proxyURL string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.ensureStatsLocked(proxyURL)
	if st == nil {
		return
	}
	st.Fail++
	st.ConsecutiveFailure++
	st.LastFailureAt = time.Now()
	if st.ConsecutiveFailure >= proxyFailureThreshold {
		power := st.ConsecutiveFailure - proxyFailureThreshold
		seconds := float64(minProxyCooldown/time.Second) * math.Pow(2, float64(power))
		cooldown := time.Duration(math.Min(seconds, float64(maxProxyCooldown/time.Second))) * time.Second
		st.CooldownUntil = time.Now().Add(cooldown)
	}
}

func (m *Manager) Statuses() []ProxyStatus {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	boundCounts := make(map[string]int)
	for _, proxy := range m.bindings {
		if proxy != "" {
			boundCounts[proxy]++
		}
	}

	out := make([]ProxyStatus, 0, len(m.proxies))
	for _, proxy := range m.proxies {
		st := m.ensureStatsLocked(proxy)
		status, left := proxyRuntimeStatus(st, now)
		out = append(out, ProxyStatus{
			ID:                 ProxyID(proxy),
			Proxy:              SafeProxy(proxy),
			Status:             status,
			CooldownLeft:       left,
			Success:            st.Success,
			Fail:               st.Fail,
			ConsecutiveFailure: st.ConsecutiveFailure,
			BoundAccounts:      boundCounts[proxy],
		})
	}
	return out
}

func (m *Manager) AccountStatus(key string) AccountProxyStatus {
	if strings.TrimSpace(key) == "" {
		return AccountProxyStatus{Status: "direct"}
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	proxyURL := m.bindings[key]
	if proxyURL == "" {
		if len(m.proxies) == 0 {
			return AccountProxyStatus{Status: "direct"}
		}
		return AccountProxyStatus{Status: "unbound"}
	}

	st := m.ensureStatsLocked(proxyURL)
	status, left := proxyRuntimeStatus(st, time.Now())
	return AccountProxyStatus{
		ID:           ProxyID(proxyURL),
		Proxy:        SafeProxy(proxyURL),
		Status:       status,
		CooldownLeft: left,
	}
}

func (m *Manager) selectProxyLocked(key string) string {
	if len(m.proxies) == 0 {
		return ""
	}

	now := time.Now()
	if bound := m.bindings[key]; bound != "" && m.proxySet[bound] {
		if !m.isCoolingLocked(bound, now) {
			return bound
		}
	}

	n := len(m.proxies)
	var fallback string
	var fallbackUntil time.Time
	for i := 0; i < n; i++ {
		idx := int(atomic.AddUint64(&m.nextIndex, 1)-1) % n
		candidate := m.proxies[idx]
		st := m.ensureStatsLocked(candidate)
		if !isCooling(st, now) {
			m.bindings[key] = candidate
			return candidate
		}
		if fallback == "" || st.CooldownUntil.Before(fallbackUntil) {
			fallback = candidate
			fallbackUntil = st.CooldownUntil
		}
	}

	if fallback == "" {
		fallback = m.proxies[0]
	}
	m.bindings[key] = fallback
	return fallback
}

func (m *Manager) clientLocked(proxyURL string, kind ClientKind) *http.Client {
	cacheKey := string(kind) + "|" + proxyURL
	if client := m.clients[cacheKey]; client != nil {
		return client
	}
	client := NewClient(proxyURL, kind)
	m.clients[cacheKey] = client
	return client
}

func (m *Manager) ensureStatsLocked(proxyURL string) *proxyStats {
	if proxyURL == "" || !m.proxySet[proxyURL] {
		return nil
	}
	st := m.stats[proxyURL]
	if st == nil {
		st = &proxyStats{}
		m.stats[proxyURL] = st
	}
	if !st.CooldownUntil.IsZero() && !time.Now().Before(st.CooldownUntil) {
		st.CooldownUntil = time.Time{}
	}
	return st
}

func (m *Manager) isCoolingLocked(proxyURL string, now time.Time) bool {
	st := m.ensureStatsLocked(proxyURL)
	return isCooling(st, now)
}

func NewClient(proxyURL string, kind ClientKind) *http.Client {
	timeout := 30 * time.Second
	maxIdle := 50
	maxIdlePerHost := 10
	if kind == KindStream {
		timeout = 5 * time.Minute
		maxIdle = 100
		maxIdlePerHost = 20
	} else if kind == KindRest {
		maxIdle = 100
		maxIdlePerHost = 20
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: BuildTransport(proxyURL, maxIdle, maxIdlePerHost),
	}
}

func BuildTransport(proxyURL string, maxIdle, maxIdlePerHost int) *http.Transport {
	t := &http.Transport{
		MaxIdleConns:        maxIdle,
		MaxIdleConnsPerHost: maxIdlePerHost,
		IdleConnTimeout:     90 * time.Second,
		DisableCompression:  false,
		ForceAttemptHTTP2:   true,
	}
	if strings.TrimSpace(proxyURL) == "" {
		t.Proxy = http.ProxyFromEnvironment
		return t
	}

	u, err := url.Parse(proxyURL)
	if err != nil {
		t.Proxy = http.ProxyFromEnvironment
		return t
	}
	if strings.EqualFold(u.Scheme, "socks5h") {
		copyURL := *u
		copyURL.Scheme = "socks5"
		u = &copyURL
	}
	t.Proxy = http.ProxyURL(u)
	t.ForceAttemptHTTP2 = false
	return t
}

func ShouldCountFailure(statusCode int, err error) bool {
	if err != nil {
		return true
	}
	return statusCode == http.StatusTooManyRequests || statusCode >= 500
}

func ShouldCountSuccess(statusCode int, err error) bool {
	if err != nil {
		return false
	}
	return statusCode > 0 && statusCode < 500 && statusCode != http.StatusTooManyRequests
}

func hasProxyScheme(text string) bool {
	idx := strings.Index(text, "://")
	if idx <= 0 {
		return false
	}
	for _, r := range text[:idx] {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '+' || r == '-' || r == '.' {
			continue
		}
		return false
	}
	return true
}

func validateProxyURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("invalid proxy %q: %w", raw, err)
	}
	scheme := strings.ToLower(u.Scheme)
	switch scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return "", fmt.Errorf("unsupported proxy scheme %q", u.Scheme)
	}
	if u.Hostname() == "" {
		return "", fmt.Errorf("invalid proxy %q: host is required", raw)
	}
	u.Scheme = scheme
	return u.String(), nil
}

func dedupeProxies(proxies []string) []string {
	seen := make(map[string]bool)
	out := make([]string, 0, len(proxies))
	for _, raw := range proxies {
		proxy, err := NormalizeProxyLine(raw)
		if err != nil || proxy == "" || seen[proxy] {
			continue
		}
		seen[proxy] = true
		out = append(out, proxy)
	}
	return out
}

func isCooling(st *proxyStats, now time.Time) bool {
	return st != nil && !st.CooldownUntil.IsZero() && now.Before(st.CooldownUntil)
}

func proxyRuntimeStatus(st *proxyStats, now time.Time) (string, int) {
	if isCooling(st, now) {
		return "cooldown", int(time.Until(st.CooldownUntil).Seconds())
	}
	return "ok", 0
}

func SortedSafeProxyList(proxies []string) []string {
	out := make([]string, 0, len(proxies))
	for _, proxy := range proxies {
		out = append(out, SafeProxy(proxy))
	}
	sort.Strings(out)
	return out
}
