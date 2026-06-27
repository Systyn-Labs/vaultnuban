// Package nomba implements the provider.Provider interface against the Nomba API.
package nomba

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

const (
	tokenEndpoint = "/v1/auth/token/issue"
	tokenTTL      = 28 * time.Minute // Nomba tokens last 30 min; refresh at 28
)

type tokenCache struct {
	mu          sync.Mutex
	accessToken string
	expiresAt   time.Time
	// singleflight-style: if a refresh is in progress, waiters block here
	inflight chan struct{}
}

// Client is a thin Nomba HTTP client with token management.
type Client struct {
	baseURL    string
	clientID   string
	clientSecret string
	accountID  string // accountId header required by Nomba
	httpClient *http.Client
	token      tokenCache
}

func NewClient(baseURL, clientID, clientSecret, accountID string) *Client {
	return &Client{
		baseURL:      baseURL,
		clientID:     clientID,
		clientSecret: clientSecret,
		accountID:    accountID,
		httpClient:   &http.Client{Timeout: 30 * time.Second},
	}
}

// accessToken returns a valid Nomba access token, refreshing if needed.
// Uses a single-flight pattern to avoid the documented concurrent-auth 401 (NFR-8).
func (c *Client) accessToken(ctx context.Context) (string, error) {
	c.token.mu.Lock()

	if c.token.accessToken != "" && time.Now().Before(c.token.expiresAt) {
		tok := c.token.accessToken
		c.token.mu.Unlock()
		return tok, nil
	}

	// If another goroutine is already refreshing, wait for it.
	if c.token.inflight != nil {
		ch := c.token.inflight
		c.token.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return "", ctx.Err()
		}
		c.token.mu.Lock()
		tok := c.token.accessToken
		c.token.mu.Unlock()
		return tok, nil
	}

	// We are the one refreshing.
	c.token.inflight = make(chan struct{})
	c.token.mu.Unlock()

	tok, err := c.fetchToken(ctx)

	c.token.mu.Lock()
	ch := c.token.inflight
	c.token.inflight = nil
	if err == nil {
		c.token.accessToken = tok
		c.token.expiresAt = time.Now().Add(tokenTTL)
	}
	c.token.mu.Unlock()

	close(ch)
	return tok, err
}

type tokenRequest struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	GrantType    string `json:"grant_type"`
}

type tokenResponse struct {
	Code        string `json:"code"`
	Description string `json:"description"`
	Data        struct {
		AccessToken string `json:"access_token"`
	} `json:"data"`
}

func (c *Client) fetchToken(ctx context.Context) (string, error) {
	body, _ := json.Marshal(tokenRequest{
		ClientID:     c.clientID,
		ClientSecret: c.clientSecret,
		GrantType:    "client_credentials",
	})

	resp, err := c.do(ctx, http.MethodPost, tokenEndpoint, body, false)
	if err != nil {
		return "", fmt.Errorf("nomba: fetch token: %w", err)
	}
	defer resp.Body.Close()

	var tr tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return "", fmt.Errorf("nomba: decode token response: %w", err)
	}
	if tr.Data.AccessToken == "" {
		return "", fmt.Errorf("nomba: empty access token (code=%s desc=%s)", tr.Code, tr.Description)
	}
	return tr.Data.AccessToken, nil
}

// do executes an HTTP request against the Nomba API.
// If auth=true, injects the Authorization header (and fetches a token if needed).
func (c *Client) do(ctx context.Context, method, path string, body []byte, auth bool) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if c.accountID != "" {
		req.Header.Set("accountId", c.accountID)
	}

	if auth {
		tok, err := c.accessToken(ctx)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, &APIError{StatusCode: resp.StatusCode, Body: b}
	}
	return resp, nil
}

// authDo is a convenience wrapper that always injects auth.
func (c *Client) authDo(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	return c.do(ctx, method, path, body, true)
}

// APIError represents a non-2xx Nomba response.
type APIError struct {
	StatusCode int
	Body       []byte
}

func (e *APIError) Error() string {
	return fmt.Sprintf("nomba: HTTP %d: %s", e.StatusCode, e.Body)
}
