package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"kiro-go/outbound"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	kiroCliLocalSessionPrefix     = "kam-kiro-cli-local-"
	kiroCliBrowserCaptureScript   = "kiro-browser-capture"
	kiroCliPathEnv                = "KIRO_CLI_PATH"
	kiroCliLoginURLFileName       = "login-url.txt"
	kiroCliLoginLogFileName       = "kiro-cli-login.log"
	kiroCliLoginPIDFileName       = "kiro-cli-login.pid"
	kiroCliLoginURLCaptureTimeout = 15 * time.Second
	kiroCliBuilderIDStartURL      = "https://view.awsapps.com/start"
	kiroCliDefaultRegion          = "us-east-1"
)

type KiroCliLocalAvailability struct {
	Available      bool   `json:"available"`
	ExecutablePath string `json:"executablePath,omitempty"`
	Version        string `json:"version,omitempty"`
	Error          string `json:"error,omitempty"`
}

type KiroCliLocalLoginSession struct {
	SessionID        string   `json:"sessionId"`
	SessionDir       string   `json:"sessionDir"`
	HomeDir          string   `json:"homeDir"`
	MachineID        string   `json:"machineId"`
	ProjectDir       string   `json:"projectDir"`
	DBPath           string   `json:"dbPath"`
	LoginURL         string   `json:"loginUrl,omitempty"`
	LoginURLCaptured bool     `json:"loginUrlCaptured"`
	ProcessID        int      `json:"processId,omitempty"`
	LoginLogPath     string   `json:"loginLogPath"`
	Warnings         []string `json:"warnings"`
}

type KiroCliLocalImportResult struct {
	SessionID       string                     `json:"sessionId"`
	AccountJSON     string                     `json:"accountJson"`
	RawSnapshotJSON string                     `json:"rawSnapshotJson"`
	MachineID       string                     `json:"machineId"`
	DBPath          string                     `json:"dbPath"`
	Warnings        []string                   `json:"warnings"`
	Accounts        []KiroCliExportableAccount `json:"accounts"`
}

type KiroCliExportableAccount struct {
	RefreshToken string `json:"refreshToken"`
	AccessToken  string `json:"accessToken"`
	Provider     string `json:"provider"`
	AuthMethod   string `json:"authMethod"`
	Region       string `json:"region"`
	MachineID    string `json:"machineId"`
	ExpiresAt    string `json:"expiresAt,omitempty"`
	ProfileArn   string `json:"profileArn,omitempty"`
	ClientID     string `json:"clientId,omitempty"`
	ClientSecret string `json:"clientSecret,omitempty"`
	StartURL     string `json:"startUrl,omitempty"`
}

type KiroCliAccount struct {
	AccessToken  string
	RefreshToken string
	ProfileArn   string
	Region       string
	ExpiresAt    string
	Scopes       []string
	AuthMethod   string
	TokenKey     string
	ClientID     string
	ClientSecret string
}

type KiroCliDBSnapshot struct {
	TokenEntries       []KiroCliAuthEntry         `json:"tokenEntries"`
	DeviceRegistration *KiroCliDeviceRegistration `json:"deviceRegistration,omitempty"`
	DBPath             string                     `json:"dbPath"`
}

type KiroCliAuthEntry struct {
	Key         string            `json:"key"`
	ValueJSON   string            `json:"valueJson"`
	ParsedToken *KiroCliTokenData `json:"parsedToken,omitempty"`
}

type KiroCliTokenData struct {
	AccessToken  string   `json:"accessToken"`
	RefreshToken string   `json:"refreshToken"`
	ExpiresAt    string   `json:"expiresAt,omitempty"`
	Region       string   `json:"region"`
	StartURL     string   `json:"startUrl,omitempty"`
	OAuthFlow    string   `json:"oauthFlow,omitempty"`
	Scopes       []string `json:"scopes,omitempty"`
}

type KiroCliDeviceRegistration struct {
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret"`
	Region       string `json:"region"`
}

type kiroCliLocalSessionPaths struct {
	SessionDir           string
	HomeDir              string
	DataHome             string
	ConfigHome           string
	CacheHome            string
	ProjectDir           string
	BrowserBinDir        string
	BrowserCaptureScript string
	LoginURLFile         string
	LoginLog             string
	LoginPIDFile         string
	DBPath               string
}

func CheckKiroCliLocalAvailable() KiroCliLocalAvailability {
	executablePath := resolveKiroCliExecutable()
	if executablePath == "" {
		return KiroCliLocalAvailability{
			Available: false,
			Error:     "未检测到 kiro-cli。Docker 部署请把宿主机 kiro-cli 挂载进容器并设置 KIRO_CLI_PATH；源码运行请确保 kiro-cli 在 PATH 中",
		}
	}

	version := ""
	if output, err := exec.Command(executablePath, "--version").Output(); err == nil {
		version = strings.TrimSpace(string(output))
	}
	if _, err := exec.LookPath("sqlite3"); err != nil {
		return KiroCliLocalAvailability{
			Available:      false,
			ExecutablePath: executablePath,
			Version:        version,
			Error:          "未检测到 sqlite3，无法读取 kiro-cli 登录数据库",
		}
	}

	return KiroCliLocalAvailability{
		Available:      true,
		ExecutablePath: executablePath,
		Version:        version,
	}
}

func StartKiroCliLocalLogin() (*KiroCliLocalLoginSession, error) {
	executablePath := resolveKiroCliExecutable()
	if executablePath == "" {
		return nil, fmt.Errorf("未检测到 kiro-cli。Docker 部署请把宿主机 kiro-cli 挂载进容器并设置 KIRO_CLI_PATH；源码运行请确保 kiro-cli 在 PATH 中")
	}
	if _, err := exec.LookPath("sqlite3"); err != nil {
		return nil, fmt.Errorf("未检测到 sqlite3，无法读取 kiro-cli 登录数据库")
	}

	if err := cleanupOldKiroCliLocalSessions(); err != nil {
		return nil, err
	}

	sessionID := strings.ReplaceAll(uuid.NewString(), "-", "")
	machineID := strings.ToLower(uuid.NewString())
	devDeviceID := strings.ToLower(uuid.NewString())
	paths, err := localKiroCliSessionPaths(sessionID)
	if err != nil {
		return nil, err
	}

	if err := prepareKiroCliLocalEnvironment(paths, machineID, devDeviceID); err != nil {
		return nil, err
	}
	if err := writeKiroCliBrowserCaptureScripts(paths); err != nil {
		return nil, err
	}

	cmd, err := spawnKiroCliLocalLogin(executablePath, paths, machineID)
	if err != nil {
		return nil, err
	}
	processID := cmd.Process.Pid
	if err := os.WriteFile(paths.LoginPIDFile, []byte(fmt.Sprint(processID)), 0600); err != nil {
		return nil, fmt.Errorf("写入登录进程 PID 失败: %w", err)
	}

	go func() {
		_ = cmd.Wait()
	}()

	warnings := []string{
		"轻量隔离使用临时 HOME/XDG 目录和 Kiro storage 机器码；不会修改系统 /etc/machine-id。",
	}
	loginURL := waitForKiroCliLoginURL(paths.LoginURLFile, paths.LoginLog)
	if loginURL == "" {
		warnings = append(warnings, fmt.Sprintf(
			"已在后台启动 Kiro CLI，但 %d 秒内没有捕获到登录链接；可稍后查看日志 %s",
			int(kiroCliLoginURLCaptureTimeout.Seconds()),
			paths.LoginLog,
		))
	}

	return &KiroCliLocalLoginSession{
		SessionID:        sessionID,
		SessionDir:       paths.SessionDir,
		HomeDir:          paths.HomeDir,
		MachineID:        machineID,
		ProjectDir:       paths.ProjectDir,
		DBPath:           paths.DBPath,
		LoginURL:         loginURL,
		LoginURLCaptured: loginURL != "",
		ProcessID:        processID,
		LoginLogPath:     paths.LoginLog,
		Warnings:         warnings,
	}, nil
}

func ExportKiroCliLocalLogin(sessionID, machineID string) (*KiroCliLocalImportResult, error) {
	if err := validateKiroCliSessionID(sessionID); err != nil {
		return nil, err
	}
	if err := validateKiroCliMachineID(machineID); err != nil {
		return nil, err
	}

	paths, err := localKiroCliSessionPaths(sessionID)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(paths.SessionDir); err != nil {
		return nil, fmt.Errorf("未找到轻量登录临时环境，请重新开始登录")
	}

	dbPath, err := detectKiroCliLocalDBPath(paths)
	if err != nil {
		return nil, err
	}

	accounts, snapshot, err := readKiroCliDB(dbPath)
	if err != nil {
		return nil, err
	}
	snapshot.DBPath = dbPath

	exportAccounts, warnings, err := buildKiroCliExportAccounts(accounts, snapshot, machineID)
	if err != nil {
		return nil, err
	}
	if len(snapshot.TokenEntries) > 1 {
		warnings = append(warnings, fmt.Sprintf(
			"CLI 数据库包含 %d 个 token 条目，账号管理 JSON 只导出当前优先账号",
			len(snapshot.TokenEntries),
		))
	}

	accountJSON, err := json.MarshalIndent(exportAccounts, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("序列化账号管理 JSON 失败: %w", err)
	}
	rawExport := map[string]interface{}{
		"machineId": machineID,
		"snapshot":  snapshot,
	}
	rawSnapshotJSON, err := json.MarshalIndent(rawExport, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("序列化 CLI 原始快照失败: %w", err)
	}

	if cleaned, cleanupErr := CleanupKiroCliLocalSession(sessionID); cleanupErr != nil {
		warnings = append(warnings, fmt.Sprintf("凭证已导出，但清理临时环境失败: %v", cleanupErr))
	} else if !cleaned {
		warnings = append(warnings, "凭证已导出，但临时环境已不存在")
	}

	return &KiroCliLocalImportResult{
		SessionID:       sessionID,
		AccountJSON:     string(accountJSON),
		RawSnapshotJSON: string(rawSnapshotJSON),
		MachineID:       machineID,
		DBPath:          dbPath,
		Warnings:        warnings,
		Accounts:        exportAccounts,
	}, nil
}

func CleanupKiroCliLocalSession(sessionID string) (bool, error) {
	paths, err := localKiroCliSessionPaths(sessionID)
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(paths.SessionDir); os.IsNotExist(err) {
		return false, nil
	}
	terminateKiroCliLocalLoginProcess(paths.LoginPIDFile)
	if err := os.RemoveAll(paths.SessionDir); err != nil {
		return false, fmt.Errorf("清理临时环境失败: %w", err)
	}
	return true, nil
}

func resolveKiroCliExecutable() string {
	candidates := kiroCliExecutableCandidates()
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if path, err := exec.LookPath(candidate); err == nil {
			return path
		}
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}

func kiroCliExecutableCandidates() []string {
	var candidates []string
	if envPath := strings.TrimSpace(os.Getenv(kiroCliPathEnv)); envPath != "" {
		candidates = append(candidates, envPath)
	}
	if runtime.GOOS == "windows" {
		if localAppData := os.Getenv("LOCALAPPDATA"); localAppData != "" {
			candidates = append(candidates, filepath.Join(localAppData, "Kiro-Cli", "kiro-cli.exe"))
		}
		candidates = append(candidates, "kiro-cli.exe")
		return candidates
	}

	candidates = append(candidates, "kiro-cli", "/usr/local/bin/kiro-cli")
	if home := os.Getenv("HOME"); home != "" {
		if runtime.GOOS == "darwin" {
			candidates = append(candidates, filepath.Join(home, "Library", "Application Support", "kiro-cli", "bin", "kiro-cli"))
		}
		candidates = append(candidates, filepath.Join(home, ".local", "bin", "kiro-cli"))
	}
	return candidates
}

func localKiroCliSessionPaths(sessionID string) (*kiroCliLocalSessionPaths, error) {
	if err := validateKiroCliSessionID(sessionID); err != nil {
		return nil, err
	}
	sessionDir := filepath.Join(os.TempDir(), kiroCliLocalSessionPrefix+sessionID)
	homeDir := filepath.Join(sessionDir, "home")
	dataHome := filepath.Join(homeDir, ".local", "share")
	configHome := filepath.Join(homeDir, ".config")
	cacheHome := filepath.Join(homeDir, ".cache")
	projectDir := filepath.Join(sessionDir, "project")
	browserBinDir := filepath.Join(sessionDir, "bin")

	return &kiroCliLocalSessionPaths{
		SessionDir:           sessionDir,
		HomeDir:              homeDir,
		DataHome:             dataHome,
		ConfigHome:           configHome,
		CacheHome:            cacheHome,
		ProjectDir:           projectDir,
		BrowserBinDir:        browserBinDir,
		BrowserCaptureScript: filepath.Join(browserBinDir, kiroCliBrowserCaptureScript),
		LoginURLFile:         filepath.Join(sessionDir, kiroCliLoginURLFileName),
		LoginLog:             filepath.Join(sessionDir, kiroCliLoginLogFileName),
		LoginPIDFile:         filepath.Join(sessionDir, kiroCliLoginPIDFileName),
		DBPath:               filepath.Join(dataHome, "kiro-cli", "data.sqlite3"),
	}, nil
}

func cleanupOldKiroCliLocalSessions() error {
	entries, err := os.ReadDir(os.TempDir())
	if err != nil {
		return fmt.Errorf("读取临时目录失败: %w", err)
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), kiroCliLocalSessionPrefix) {
			continue
		}
		sessionID := strings.TrimPrefix(entry.Name(), kiroCliLocalSessionPrefix)
		if _, err := CleanupKiroCliLocalSession(sessionID); err != nil {
			_ = os.RemoveAll(filepath.Join(os.TempDir(), entry.Name()))
		}
	}
	return nil
}

func prepareKiroCliLocalEnvironment(paths *kiroCliLocalSessionPaths, machineID, devDeviceID string) error {
	storage := map[string]string{
		"telemetry.machineId":   sha256Hex(machineID),
		"telemetry.devDeviceId": devDeviceID,
	}
	storageJSON, err := json.MarshalIndent(storage, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化轻量环境机器码配置失败: %w", err)
	}

	dirs := []string{
		paths.HomeDir,
		filepath.Join(paths.DataHome, "kiro-cli"),
		filepath.Join(paths.ConfigHome, "Kiro", "User", "globalStorage"),
		filepath.Join(paths.ConfigHome, "kiro", "User", "globalStorage"),
		paths.CacheHome,
		paths.ProjectDir,
		paths.BrowserBinDir,
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("创建临时环境目录失败 %s: %w", dir, err)
		}
	}

	writes := map[string][]byte{
		filepath.Join(paths.ConfigHome, "Kiro", "machineid"):                             machineIDBytes(machineID),
		filepath.Join(paths.ConfigHome, "kiro", "machineid"):                             machineIDBytes(machineID),
		filepath.Join(paths.ConfigHome, "Kiro", "User", "globalStorage", "storage.json"): storageJSON,
		filepath.Join(paths.ConfigHome, "kiro", "User", "globalStorage", "storage.json"): storageJSON,
	}
	for path, data := range writes {
		if err := os.WriteFile(path, data, 0600); err != nil {
			return fmt.Errorf("写入临时环境文件失败 %s: %w", path, err)
		}
	}
	return nil
}

func machineIDBytes(machineID string) []byte {
	return []byte(machineID)
}

func writeKiroCliBrowserCaptureScripts(paths *kiroCliLocalSessionPaths) error {
	script := fmt.Sprintf(`#!/bin/sh
for arg in "$@"; do
  case "$arg" in
    http://*|https://*)
      printf '%%s\n' "$arg" > %s
      exit 0
      ;;
  esac
done
printf '%%s\n' "$*" >> %s
exit 0
`, shellQuote(paths.LoginURLFile), shellQuote(paths.LoginURLFile))

	if err := os.WriteFile(paths.BrowserCaptureScript, []byte(script), 0755); err != nil {
		return fmt.Errorf("写入登录链接捕获脚本失败: %w", err)
	}

	for _, name := range kiroCliBrowserCaptureAliases() {
		aliasPath := filepath.Join(paths.BrowserBinDir, name)
		_ = os.Remove(aliasPath)
		if runtime.GOOS == "windows" {
			continue
		}
		if err := os.Symlink(paths.BrowserCaptureScript, aliasPath); err != nil {
			return fmt.Errorf("创建浏览器捕获别名失败 %s: %w", aliasPath, err)
		}
	}
	return nil
}

func kiroCliBrowserCaptureAliases() []string {
	if runtime.GOOS != "linux" {
		return nil
	}
	return []string{"xdg-open", "sensible-browser", "gio", "gnome-open", "kde-open", "wslview"}
}

func spawnKiroCliLocalLogin(executablePath string, paths *kiroCliLocalSessionPaths, machineID string) (*exec.Cmd, error) {
	logFile, err := os.Create(paths.LoginLog)
	if err != nil {
		return nil, fmt.Errorf("创建 CLI 登录日志失败: %w", err)
	}
	stderrLog, err := os.OpenFile(paths.LoginLog, os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		_ = logFile.Close()
		return nil, fmt.Errorf("打开 CLI 登录日志失败: %w", err)
	}

	cmd := exec.Command(executablePath, "login")
	cmd.Dir = paths.ProjectDir
	cmd.Env = append(os.Environ(),
		"HOME="+paths.HomeDir,
		"USER="+defaultUser(),
		"XDG_DATA_HOME="+paths.DataHome,
		"XDG_CONFIG_HOME="+paths.ConfigHome,
		"XDG_CACHE_HOME="+paths.CacheHome,
		"KIRO_MACHINE_ID="+machineID,
		"VSCODE_MACHINE_ID="+sha256Hex(machineID),
		"DBUS_MACHINE_ID="+strings.ReplaceAll(machineID, "-", ""),
		"BROWSER="+paths.BrowserCaptureScript,
		"PATH="+prependPath(paths.BrowserBinDir, os.Getenv("PATH")),
	)
	if outbound.Enabled() {
		if lease := outbound.Acquire("kiro-cli-local", outbound.KindAuth); lease != nil && lease.ProxyURL != "" {
			cmd.Env = append(cmd.Env,
				"HTTP_PROXY="+lease.ProxyURL,
				"HTTPS_PROXY="+lease.ProxyURL,
				"ALL_PROXY="+lease.ProxyURL,
				"http_proxy="+lease.ProxyURL,
				"https_proxy="+lease.ProxyURL,
				"all_proxy="+lease.ProxyURL,
			)
		}
	}
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = stderrLog

	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		_ = stderrLog.Close()
		return nil, fmt.Errorf("后台启动 Kiro CLI 登录失败: %w", err)
	}
	return cmd, nil
}

func defaultUser() string {
	if user := os.Getenv("USER"); user != "" {
		return user
	}
	return "kiro"
}

func prependPath(first, current string) string {
	if current == "" {
		return first
	}
	return first + string(os.PathListSeparator) + current
}

func waitForKiroCliLoginURL(loginURLFile, loginLog string) string {
	deadline := time.Now().Add(kiroCliLoginURLCaptureTimeout)
	for time.Now().Before(deadline) {
		if url := readFirstHTTPURL(loginURLFile); url != "" {
			return url
		}
		if url := readFirstHTTPURL(loginLog); url != "" {
			return url
		}
		time.Sleep(250 * time.Millisecond)
	}
	if url := readFirstHTTPURL(loginURLFile); url != "" {
		return url
	}
	return readFirstHTTPURL(loginLog)
}

func readFirstHTTPURL(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return extractFirstHTTPURL(string(data))
}

func extractFirstHTTPURL(text string) string {
	start := strings.Index(text, "https://")
	if start < 0 {
		start = strings.Index(text, "http://")
	}
	if start < 0 {
		return ""
	}
	tail := text[start:]
	end := len(tail)
	for i, r := range tail {
		if i == 0 {
			continue
		}
		if r == '"' || r == '\'' || r == '<' || r == '>' || r == ')' || r == ']' || r == ',' || r == ';' || r == '\n' || r == '\r' || r == '\t' || r == ' ' {
			end = i
			break
		}
	}
	return strings.Trim(tail[:end], "\"',;")
}

func terminateKiroCliLocalLoginProcess(pidFile string) {
	data, err := os.ReadFile(pidFile)
	if err != nil {
		return
	}
	pid := strings.TrimSpace(string(data))
	if pid == "" {
		return
	}
	if runtime.GOOS == "windows" {
		_ = exec.Command("taskkill", "/PID", pid, "/F").Run()
		return
	}
	_ = exec.Command("kill", pid).Run()
}

func detectKiroCliLocalDBPath(paths *kiroCliLocalSessionPaths) (string, error) {
	candidates := []string{
		paths.DBPath,
		filepath.Join(paths.HomeDir, ".local", "share", "kiro-cli", "data.sqlite3"),
		filepath.Join(paths.HomeDir, "Library", "Application Support", "kiro-cli", "data.sqlite3"),
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("临时环境内未找到 kiro-cli 数据库，请确认已在浏览器完成登录")
}

func readKiroCliDB(dbPath string) ([]KiroCliAccount, *KiroCliDBSnapshot, error) {
	rows, err := readKiroCliAuthRows(dbPath)
	if err != nil {
		return nil, nil, err
	}
	accounts, snapshot := buildKiroCliAccountsFromRows(rows, dbPath)
	if len(accounts) == 0 {
		return nil, nil, fmt.Errorf("未找到有效的账号数据")
	}
	return accounts, snapshot, nil
}

type kiroCliAuthRow struct {
	Key   string
	Value string
}

func readKiroCliAuthRows(dbPath string) ([]kiroCliAuthRow, error) {
	sqlitePath, err := exec.LookPath("sqlite3")
	if err != nil {
		return nil, fmt.Errorf("未检测到 sqlite3，无法读取 kiro-cli 登录数据库")
	}

	output, err := exec.Command(sqlitePath, "-json", dbPath, "SELECT key, value FROM auth_kv").Output()
	if err != nil {
		return nil, fmt.Errorf("查询 auth_kv 失败: %w", err)
	}
	var result []kiroCliAuthRow
	if err := json.Unmarshal(output, &result); err != nil {
		return nil, fmt.Errorf("解析 auth_kv 查询结果失败: %w", err)
	}
	return result, nil
}

func buildKiroCliAccountsFromRows(rows []kiroCliAuthRow, dbPath string) ([]KiroCliAccount, *KiroCliDBSnapshot) {
	rowMap := make(map[string]string)
	for _, row := range rows {
		rowMap[row.Key] = row.Value
	}

	snapshot := &KiroCliDBSnapshot{DBPath: dbPath}
	for _, row := range rows {
		if strings.HasSuffix(row.Key, "token") {
			entry := KiroCliAuthEntry{Key: row.Key, ValueJSON: row.Value}
			if token, err := parseKiroCliTokenData(row.Value); err == nil {
				entry.ParsedToken = token
			}
			snapshot.TokenEntries = append(snapshot.TokenEntries, entry)
		}
	}
	if device := readKiroCliDeviceRegistration(rowMap); device != nil {
		snapshot.DeviceRegistration = device
	}

	var accounts []KiroCliAccount
	for _, key := range []string{"kirocli:social:token", "kirocli:odic:token", "codewhisperer:odic:token"} {
		value := rowMap[key]
		if value == "" {
			continue
		}
		account, err := parseKiroCliAccount(key, value)
		if err != nil {
			continue
		}
		if account.AuthMethod == "IdC" && snapshot.DeviceRegistration != nil {
			account.ClientID = snapshot.DeviceRegistration.ClientID
			account.ClientSecret = snapshot.DeviceRegistration.ClientSecret
		}
		accounts = append(accounts, account)
		break
	}
	return accounts, snapshot
}

func parseKiroCliAccount(key, value string) (KiroCliAccount, error) {
	var raw map[string]interface{}
	if err := json.Unmarshal([]byte(value), &raw); err != nil {
		return KiroCliAccount{}, err
	}
	accessToken, _ := raw["access_token"].(string)
	refreshToken, _ := raw["refresh_token"].(string)
	if accessToken == "" || refreshToken == "" {
		return KiroCliAccount{}, fmt.Errorf("token missing access_token or refresh_token")
	}
	region, _ := raw["region"].(string)
	if region == "" {
		region = kiroCliDefaultRegion
	}
	expiresAt, _ := raw["expires_at"].(string)
	profileArn, _ := raw["profile_arn"].(string)
	scopes := stringSlice(raw["scopes"])

	authMethod := "unknown"
	if profileArn != "" {
		authMethod = "social"
	} else if len(scopes) > 0 {
		authMethod = "IdC"
	}

	return KiroCliAccount{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ProfileArn:   profileArn,
		Region:       region,
		ExpiresAt:    expiresAt,
		Scopes:       scopes,
		AuthMethod:   authMethod,
		TokenKey:     key,
	}, nil
}

func parseKiroCliTokenData(value string) (*KiroCliTokenData, error) {
	var raw map[string]interface{}
	if err := json.Unmarshal([]byte(value), &raw); err != nil {
		return nil, err
	}
	region, _ := raw["region"].(string)
	if region == "" {
		region = kiroCliDefaultRegion
	}
	token := &KiroCliTokenData{
		AccessToken:  stringValue(raw["access_token"]),
		RefreshToken: stringValue(raw["refresh_token"]),
		ExpiresAt:    stringValue(raw["expires_at"]),
		Region:       region,
		StartURL:     stringValue(raw["start_url"]),
		OAuthFlow:    stringValue(raw["oauth_flow"]),
		Scopes:       stringSlice(raw["scopes"]),
	}
	return token, nil
}

func readKiroCliDeviceRegistration(rowMap map[string]string) *KiroCliDeviceRegistration {
	for _, key := range []string{"kirocli:odic:device-registration", "codewhisperer:odic:device-registration"} {
		value := rowMap[key]
		if value == "" {
			continue
		}
		var raw map[string]interface{}
		if err := json.Unmarshal([]byte(value), &raw); err != nil {
			continue
		}
		clientID := stringValue(raw["client_id"])
		clientSecret := stringValue(raw["client_secret"])
		if clientID == "" || clientSecret == "" {
			continue
		}
		region := stringValue(raw["region"])
		if region == "" {
			region = kiroCliDefaultRegion
		}
		return &KiroCliDeviceRegistration{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			Region:       region,
		}
	}
	return nil
}

func buildKiroCliExportAccounts(accounts []KiroCliAccount, snapshot *KiroCliDBSnapshot, machineID string) ([]KiroCliExportableAccount, []string, error) {
	if len(accounts) == 0 {
		return nil, nil, fmt.Errorf("数据库中没有账号数据")
	}

	account := accounts[0]
	var warnings []string
	startURL := findKiroCliStartURL(snapshot, account.TokenKey)
	provider := determineKiroCliExportProvider(account, startURL, &warnings)
	authMethod := "IdC"
	if account.AuthMethod == "social" {
		authMethod = "social"
	}

	clientID := account.ClientID
	clientSecret := account.ClientSecret
	if snapshot.DeviceRegistration != nil {
		if clientID == "" {
			clientID = snapshot.DeviceRegistration.ClientID
		}
		if clientSecret == "" {
			clientSecret = snapshot.DeviceRegistration.ClientSecret
		}
	}

	return []KiroCliExportableAccount{{
		RefreshToken: account.RefreshToken,
		AccessToken:  account.AccessToken,
		Provider:     provider,
		AuthMethod:   authMethod,
		Region:       account.Region,
		MachineID:    machineID,
		ExpiresAt:    account.ExpiresAt,
		ProfileArn:   account.ProfileArn,
		ClientID:     clientID,
		ClientSecret: clientSecret,
		StartURL:     startURL,
	}}, warnings, nil
}

func findKiroCliStartURL(snapshot *KiroCliDBSnapshot, tokenKey string) string {
	if snapshot == nil {
		return ""
	}
	for _, entry := range snapshot.TokenEntries {
		if entry.Key == tokenKey && entry.ParsedToken != nil {
			return strings.TrimSpace(entry.ParsedToken.StartURL)
		}
	}
	return ""
}

func determineKiroCliExportProvider(account KiroCliAccount, startURL string, warnings *[]string) string {
	if account.AuthMethod == "social" {
		lowerProfileArn := strings.ToLower(account.ProfileArn)
		if strings.Contains(lowerProfileArn, "github") {
			return "Github"
		}
		if strings.Contains(lowerProfileArn, "google") {
			return "Google"
		}
		*warnings = append(*warnings, "无法从 CLI 数据判断社交登录 provider，已按 Google 导出")
		return "Google"
	}

	if startURL != "" && startURL != kiroCliBuilderIDStartURL {
		return "Enterprise"
	}
	return "BuilderId"
}

func stringValue(value interface{}) string {
	text, _ := value.(string)
	return text
}

func stringSlice(value interface{}) []string {
	values, ok := value.([]interface{})
	if !ok {
		return nil
	}
	result := make([]string, 0, len(values))
	for _, item := range values {
		if text, ok := item.(string); ok {
			result = append(result, text)
		}
	}
	return result
}

func validateKiroCliSessionID(sessionID string) error {
	if len(sessionID) != 32 {
		return fmt.Errorf("无效的隔离登录 sessionId")
	}
	for _, r := range sessionID {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
			return fmt.Errorf("无效的隔离登录 sessionId")
		}
	}
	return nil
}

func validateKiroCliMachineID(machineID string) error {
	if _, err := uuid.Parse(machineID); err != nil {
		return fmt.Errorf("无效的 machineId")
	}
	return nil
}

func sha256Hex(input string) string {
	sum := sha256.Sum256([]byte(input))
	return hex.EncodeToString(sum[:])
}

func shellQuote(input string) string {
	return "'" + strings.ReplaceAll(input, "'", "'\\''") + "'"
}

func copyFile(dst, src string) error {
	source, err := os.Open(src)
	if err != nil {
		return err
	}
	defer source.Close()

	target, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer target.Close()

	_, err = io.Copy(target, source)
	return err
}
