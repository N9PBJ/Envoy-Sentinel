package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCloudAuthenticatorToken(t *testing.T) {
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	defer server.Close()

	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1024); err != nil {
			t.Fatalf("parse login form: %v", err)
		}
		if got := r.FormValue("user[email]"); got != "owner@example.com" {
			t.Errorf("email=%q", got)
		}
		if got := r.FormValue("user[password]"); got != "secret" {
			t.Errorf("password=%q", got)
		}
		_, _ = w.Write([]byte(`{"session_id":"session-123"}`))
	})
	mux.HandleFunc("/tokens", func(w http.ResponseWriter, r *http.Request) {
		var request map[string]string
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode token request: %v", err)
		}
		if request["session_id"] != "session-123" || request["serial_num"] != "serial-456" || request["username"] != "owner@example.com" {
			t.Errorf("token request=%v", request)
		}
		_, _ = w.Write([]byte("header.payload.signature"))
	})

	auth := &CloudAuthenticator{HTTPClient: server.Client(), LoginURL: server.URL + "/login", TokenURL: server.URL + "/tokens"}
	token, err := auth.Token(context.Background(), "owner@example.com", "secret", "serial-456")
	if err != nil {
		t.Fatal(err)
	}
	if token != "header.payload.signature" {
		t.Fatalf("token=%q", token)
	}
}

func TestClientRefreshesTokenAfterUnauthorizedResponse(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("Authorization") != "Bearer fresh-token" {
			http.Error(w, "expired", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "expired-token", false)
	if err != nil {
		t.Fatal(err)
	}
	refreshes := 0
	client.SetTokenProvider(func(context.Context) (string, error) {
		refreshes++
		return "fresh-token", nil
	})
	var response map[string]any
	if err := client.getJSON(context.Background(), "/data", true, &response, false); err != nil {
		t.Fatal(err)
	}
	if requests != 2 || refreshes != 1 {
		t.Fatalf("requests=%d refreshes=%d", requests, refreshes)
	}
}

func TestParseTokenFormats(t *testing.T) {
	for _, test := range []struct {
		body string
		want string
	}{
		{body: "raw.jwt.token", want: "raw.jwt.token"},
		{body: `"quoted.jwt.token"`, want: "quoted.jwt.token"},
		{body: `{"token":"object.jwt.token"}`, want: "object.jwt.token"},
	} {
		t.Run(strings.ReplaceAll(test.want, ".", "_"), func(t *testing.T) {
			got, err := parseToken([]byte(test.body))
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("got=%q want=%q", got, test.want)
			}
		})
	}
}
