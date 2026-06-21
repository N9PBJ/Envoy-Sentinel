package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
)

const (
	defaultLoginURL = "https://enlighten.enphaseenergy.com/login/login.json?"
	defaultTokenURL = "https://entrez.enphaseenergy.com/tokens"
)

// CloudAuthenticator implements the Enphase token flow documented in
// TEB-00060. Endpoint fields are exported so the flow can be tested without
// contacting Enphase.
type CloudAuthenticator struct {
	HTTPClient *http.Client
	LoginURL   string
	TokenURL   string
}

func NewCloudAuthenticator() *CloudAuthenticator {
	return &CloudAuthenticator{
		HTTPClient: &http.Client{Timeout: 30 * time.Second},
		LoginURL:   defaultLoginURL,
		TokenURL:   defaultTokenURL,
	}
}

// Token exchanges the owner's Enphase credentials for a bearer token scoped
// to one IQ Gateway serial number. Credentials and returned tokens are kept in
// memory only; callers must take care not to log them.
func (a *CloudAuthenticator) Token(ctx context.Context, username, password, serialNumber string) (string, error) {
	if strings.TrimSpace(username) == "" || password == "" || strings.TrimSpace(serialNumber) == "" {
		return "", fmt.Errorf("enphase username, password, and gateway serial number are required")
	}

	sessionID, err := a.login(ctx, username, password)
	if err != nil {
		return "", err
	}

	payload, err := json.Marshal(map[string]string{
		"session_id": sessionID,
		"serial_num": serialNumber,
		"username":   username,
	})
	if err != nil {
		return "", fmt.Errorf("encode Enphase token request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.TokenURL, bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("create Enphase token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	body, err := a.do(req, "request Enphase gateway token")
	if err != nil {
		return "", err
	}
	token, err := parseToken(body)
	if err != nil {
		return "", fmt.Errorf("decode Enphase gateway token: %w", err)
	}
	return token, nil
}

func (a *CloudAuthenticator) login(ctx context.Context, username, password string) (string, error) {
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	if err := form.WriteField("user[email]", username); err != nil {
		return "", fmt.Errorf("encode Enphase login email: %w", err)
	}
	if err := form.WriteField("user[password]", password); err != nil {
		return "", fmt.Errorf("encode Enphase login password: %w", err)
	}
	if err := form.Close(); err != nil {
		return "", fmt.Errorf("finish Enphase login form: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.LoginURL, &body)
	if err != nil {
		return "", fmt.Errorf("create Enphase login request: %w", err)
	}
	req.Header.Set("Content-Type", form.FormDataContentType())

	responseBody, err := a.do(req, "log in to Enphase")
	if err != nil {
		return "", err
	}
	var response struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(responseBody, &response); err != nil {
		return "", fmt.Errorf("decode Enphase login response: %w", err)
	}
	if strings.TrimSpace(response.SessionID) == "" {
		return "", fmt.Errorf("enphase login response did not contain a session_id")
	}
	return response.SessionID, nil
}

func (a *CloudAuthenticator) do(req *http.Request, action string) ([]byte, error) {
	client := a.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", action, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("%s: read response: %w", action, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s: Enphase returned %s: %s", action, resp.Status, strings.TrimSpace(string(body)))
	}
	return body, nil
}

func parseToken(body []byte) (string, error) {
	// Enphase deployments have returned a JSON string, a JSON object, and plain
	// text across API versions. Accept all three without treating HTML error
	// pages as tokens.
	raw := strings.TrimSpace(string(body))
	if raw == "" {
		return "", fmt.Errorf("empty response")
	}

	var tokenString string
	if json.Unmarshal(body, &tokenString) == nil && strings.TrimSpace(tokenString) != "" {
		return strings.TrimSpace(tokenString), nil
	}
	var object map[string]any
	if json.Unmarshal(body, &object) == nil {
		for _, key := range []string{"token", "access_token"} {
			if token, ok := object[key].(string); ok && strings.TrimSpace(token) != "" {
				return strings.TrimSpace(token), nil
			}
		}
		return "", fmt.Errorf("JSON response did not contain a token")
	}
	if strings.HasPrefix(raw, "<") {
		return "", fmt.Errorf("unexpected HTML response")
	}
	return raw, nil
}
