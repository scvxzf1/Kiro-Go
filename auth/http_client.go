// Package auth 提供认证相关功能的 HTTP 客户端
package auth

import (
	"io"
	"kiro-go/config"
	"kiro-go/outbound"
	"net/http"
	"sync/atomic"
	"time"
)

// 全局 HTTP 客户端存储，支持运行时代理重配置
var httpClientStore atomic.Pointer[http.Client]

// httpClient 返回当前全局 auth HTTP 客户端
func httpClient() *http.Client {
	return httpClientStore.Load()
}

func outboundHTTPClient(account *config.Account) (*outbound.ClientLease, *http.Client) {
	if !outbound.Enabled() {
		return nil, httpClient()
	}
	key := "auth"
	if account != nil && account.ID != "" {
		key = account.ID
	}
	lease := outbound.Acquire(key, outbound.KindAuth)
	return lease, lease.Client
}

func doAuthRequest(req *http.Request) (*http.Response, error) {
	return doAccountAuthRequest(nil, req)
}

func doAccountAuthRequest(account *config.Account, req *http.Request) (*http.Response, error) {
	lease, client := outboundHTTPClient(account)
	resp, err := client.Do(req)
	if lease == nil {
		return resp, err
	}
	if err != nil {
		lease.RecordFailure()
		return resp, err
	}
	resp.Body = &trackedResponseBody{
		ReadCloser: resp.Body,
		lease:      lease,
		statusCode: resp.StatusCode,
	}
	return resp, nil
}

func init() {
	InitHttpClient("")
}

// buildAuthTransport 构建带可选代理的 Transport
func buildAuthTransport(proxyURL string) *http.Transport {
	return outbound.BuildTransport(proxyURL, 50, 10)
}

// InitHttpClient 初始化（或重新初始化）auth 模块的全局 HTTP 客户端
func InitHttpClient(proxyURL string) {
	client := &http.Client{
		Timeout:   30 * time.Second,
		Transport: buildAuthTransport(proxyURL),
	}
	httpClientStore.Store(client)
}

type trackedResponseBody struct {
	io.ReadCloser
	lease      *outbound.ClientLease
	statusCode int
	recorded   bool
}

func (b *trackedResponseBody) Close() error {
	err := b.ReadCloser.Close()
	if !b.recorded {
		b.recorded = true
		if outbound.ShouldCountFailure(b.statusCode, nil) {
			b.lease.RecordFailure()
		} else if outbound.ShouldCountSuccess(b.statusCode, nil) {
			b.lease.RecordSuccess()
		}
	}
	return err
}
