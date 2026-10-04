// Copyright 2026 eat-pray-ai & OpenWaygate
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"testing/iotest"
	"testing/synctest"
	"time"

	"github.com/eat-pray-ai/yutu/pkg"
	"golang.org/x/oauth2"
	"google.golang.org/api/youtube/v3"
)

// helper to build a minimal valid Google OAuth2 credential JSON.
func validCredentialJSON(redirectURL string) string {
	return validCredentialJSONWithTokenURL(redirectURL, "https://oauth2.googleapis.com/token")
}

func validCredentialJSONWithTokenURL(redirectURL string, tokenURL string) string {
	cred := map[string]map[string]any{
		"installed": {
			"client_id":                   "test-client-id",
			"project_id":                  "test-project",
			"auth_uri":                    "https://accounts.google.com/o/oauth2/auth",
			"token_uri":                   tokenURL,
			"auth_provider_x509_cert_url": "https://www.googleapis.com/oauth2/v1/certs",
			"client_secret":               "test-secret",
			"redirect_uris":               []string{redirectURL},
		},
	}
	b, _ := json.Marshal(cred)
	return string(b)
}

func TestGetConfig_Success(t *testing.T) {
	s := NewY2BService(
		WithCredential(validCredentialJSON("http://localhost"), os.DirFS(".")),
	).(*svc)

	config, err := s.getConfig()
	if err != nil {
		t.Fatalf("getConfig returned error: %v", err)
	}

	if config.ClientID != "test-client-id" {
		t.Errorf(
			"unexpected ClientID: got %q, want %q", config.ClientID, "test-client-id",
		)
	}
	if config.ClientSecret != "test-secret" {
		t.Errorf(
			"unexpected ClientSecret: got %q, want %q", config.ClientSecret,
			"test-secret",
		)
	}
	if config.RedirectURL != "http://localhost" {
		t.Errorf(
			"unexpected RedirectURL: got %q, want %q", config.RedirectURL,
			"http://localhost",
		)
	}
	if len(config.Scopes) == 0 {
		t.Fatalf("expected scopes to be populated, got none")
	}
}

func TestGetConfig_WithRedirectURL(t *testing.T) {
	s := NewY2BService(
		WithCredential(validCredentialJSON("http://localhost"), os.DirFS(".")),
		WithRedirectURL("http://localhost:8216"),
	).(*svc)

	config, err := s.getConfig()
	if err != nil {
		t.Fatalf("getConfig returned error: %v", err)
	}

	if config.RedirectURL != "http://localhost:8216" {
		t.Errorf(
			"unexpected RedirectURL: got %q, want %q", config.RedirectURL,
			"http://localhost:8216",
		)
	}
}

func TestGetConfig_InvalidJSON(t *testing.T) {
	s := NewY2BService(
		WithCredential("not-a-json", os.DirFS(".")),
	).(*svc)

	_, err := s.getConfig()
	if err == nil {
		t.Fatalf("expected error from getConfig with invalid JSON, got nil")
	}
	if err.Error() == "" {
		t.Fatalf("expected non-empty error message")
	}
}

func TestGetService_InitError(t *testing.T) {
	s := NewY2BService(WithCredential("not-a-json", os.DirFS("."))).(*svc)
	if _, err := s.GetService(); err == nil {
		t.Fatal("expected init error, got nil")
	}
}

func TestGetService_WithValidCachedToken(t *testing.T) {
	expiry := time.Now().Add(time.Hour).Format(time.RFC3339)
	s := NewY2BService(
		WithCredential(validCredentialJSON("http://localhost"), os.DirFS(".")),
		WithCacheToken(`{"access_token":"cached-token","token_type":"Bearer","expiry":"`+expiry+`"}`, os.DirFS(".")),
	).(*svc)

	service, err := s.GetService()
	if err != nil {
		t.Fatalf("GetService() error = %v", err)
	}
	if service == nil {
		t.Fatal("GetService() returned nil service")
	}
}

func TestRefreshClient_InvalidCachedTokenWithoutTokenFile(t *testing.T) {
	s := NewY2BService(
		WithCredential(validCredentialJSON("http://localhost"), os.DirFS(".")),
		WithCacheToken(`{"access_token":"expired-token","token_type":"Bearer","expiry":"2000-01-01T00:00:00Z"}`, os.DirFS(".")),
	).(*svc)

	_, err := s.refreshClient()
	if err == nil {
		t.Fatal("expected refresh error, got nil")
	}
	if !strings.Contains(err.Error(), refreshTokenFailed) {
		t.Errorf("refreshClient() error = %v, want %q", err, refreshTokenFailed)
	}
}

func TestWithCredential_FromEnv(t *testing.T) {
	credential := validCredentialJSON("http://localhost")
	t.Setenv("YUTU_CREDENTIAL", credential)

	s := NewY2BService(WithCredential("", os.DirFS("."))).(*svc)
	if s.Credential != credential {
		t.Errorf("Credential = %q, want env credential", s.Credential)
	}
	if s.initErr != nil {
		t.Errorf("initErr = %v, want nil", s.initErr)
	}
}

func TestWithCredential_Base64(t *testing.T) {
	credential := validCredentialJSON("http://localhost")
	s := NewY2BService(
		WithCredential(base64.StdEncoding.EncodeToString([]byte(credential)), os.DirFS(".")),
	).(*svc)

	if s.Credential != credential {
		t.Errorf("Credential = %q, want decoded credential", s.Credential)
	}
}

func TestWithCacheToken_FromEnv(t *testing.T) {
	token := `{"access_token":"env-token"}`
	t.Setenv("YUTU_CACHE_TOKEN", token)

	s := NewY2BService(WithCacheToken("", os.DirFS("."))).(*svc)
	if s.CacheToken != token {
		t.Errorf("CacheToken = %q, want env token", s.CacheToken)
	}
}

func TestWithCacheToken_Base64AndJSON(t *testing.T) {
	for _, tt := range []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "base64",
			input: base64.StdEncoding.EncodeToString([]byte(`{"access_token":"b64-token"}`)),
			want:  `{"access_token":"b64-token"}`,
		},
		{
			name:  "json",
			input: `{"access_token":"json-token"}`,
			want:  `{"access_token":"json-token"}`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := NewY2BService(WithCacheToken(tt.input, os.DirFS("."))).(*svc)
			if s.CacheToken != tt.want {
				t.Errorf("CacheToken = %q, want %q", s.CacheToken, tt.want)
			}
		})
	}
}

func TestStartWebServer_InvalidURL(t *testing.T) {
	s := NewY2BService().(*svc)
	s.state = "state"

	_, _, err := s.startWebServer("://bad-url")
	if err == nil {
		t.Fatalf("expected error for invalid URL, got nil")
	}
	if err.Error() == "" {
		t.Fatalf("expected non-empty error message")
	}
}

func TestStartWebServer_PortConflict(t *testing.T) {
	// Occupy a TCP port first.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen on random port: %v", err)
	}
	defer func() { _ = ln.Close() }()

	addr := ln.Addr().String()
	redirectURL := fmt.Sprintf("http://%s", addr)

	s := NewY2BService().(*svc)
	s.state = "state"

	_, _, err = s.startWebServer(redirectURL)
	if err == nil {
		t.Fatalf("expected error due to port conflict, got nil")
	}
}

func TestStartWebServer_StateMismatch(t *testing.T) {
	// Use an ephemeral port so startWebServer succeeds.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen on random port: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	redirectURL := fmt.Sprintf("http://%s", addr)

	s := NewY2BService().(*svc)
	s.state = "expected-state"
	codeCh, closeServer, err := s.startWebServer(redirectURL)
	if err != nil {
		t.Fatalf("startWebServer returned error: %v", err)
	}
	defer func() { _ = closeServer() }()

	// Send a request with mismatched state.
	resp, err := http.Get(redirectURL + "/?state=wrong&code=code123")
	if err != nil {
		t.Fatalf("failed to send HTTP request: %v", err)
	}
	_ = resp.Body.Close()

	select {
	case <-codeCh:
		t.Fatalf("expected no code to be sent on state mismatch")
	case <-time.After(100 * time.Millisecond):
		// ok: channel should remain empty
	}
}

func TestGetCodeFromPrompt_Success(t *testing.T) {
	var out bytes.Buffer
	in := strings.NewReader("some-code")

	s := NewY2BService(WithIO(in, &out)).(*svc)

	code, err := s.getCodeFromPrompt(
		"http://example.com/auth", "http://localhost:8216",
	)
	if err != nil {
		t.Fatalf("getCodeFromPrompt returned error: %v", err)
	}
	if code != "some-code" {
		t.Fatalf("unexpected code: got %q, want %q", code, "some-code")
	}

	outStr := out.String()
	if !strings.Contains(
		outStr, openBrowserHint[:20],
	) { // basic sanity check that hint was written
		t.Fatalf("expected openBrowserHint to be written to out, got %q", outStr)
	}
	if !strings.Contains(outStr, "After completing the authorization flow") {
		t.Fatalf("expected manualInputHint to be written to out, got %q", outStr)
	}
	if !strings.Contains(outStr, "http://localhost:8216/?state=DONOT-COPY") {
		t.Fatalf("expected redirect URL in manualInputHint, got %q", outStr)
	}
}

func TestGetCodeFromPrompt_URLDecode(t *testing.T) {
	var out bytes.Buffer
	in := strings.NewReader("4%2Fabc")

	s := NewY2BService(WithIO(in, &out)).(*svc)

	code, err := s.getCodeFromPrompt(
		"http://example.com/auth", "http://localhost:8216",
	)
	if err != nil {
		t.Fatalf("getCodeFromPrompt returned error: %v", err)
	}
	if code != "4/abc" {
		t.Fatalf("unexpected code: got %q, want %q", code, "4/abc")
	}
}

func TestSaveToken_InvalidPath(t *testing.T) {
	// Point pkg.Root to a directory without write permission so that
	// the underlying filesystem operations fail.
	tmpDir := t.TempDir()
	if err := os.Chmod(tmpDir, 0o555); err != nil {
		t.Fatalf("failed to chmod temp dir: %v", err)
	}

	origRootDir := *pkg.RootDir
	origRoot := pkg.Root
	defer func() {
		pkg.RootDir = &origRootDir
		pkg.Root = origRoot
	}()

	pkg.RootDir = &tmpDir
	root, err := os.OpenRoot(tmpDir)
	if err != nil {
		t.Fatalf("failed to open root: %v", err)
	}
	pkg.Root = root

	s := NewY2BService().(*svc)
	s.tokenFile = "token.json"
	tok := &oauth2.Token{AccessToken: "abc"}

	err = s.saveToken(tok)
	if err == nil {
		t.Fatalf("expected error from saveToken with invalid path, got nil")
	}
}

func TestStartWebServer_Success(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	redirectURL := fmt.Sprintf("http://%s", addr)
	s := NewY2BService().(*svc)
	s.state = "test-state"

	codeCh, closeServer, err := s.startWebServer(redirectURL)
	if err != nil {
		t.Fatalf("startWebServer error: %v", err)
	}
	defer func() { _ = closeServer() }()

	// Send the HTTP request in a goroutine to avoid deadlock:
	// the handler blocks on codeCh <- code until someone reads from codeCh.
	go func() {
		resp, err := http.Get(redirectURL + "/?state=test-state&code=auth-code-123")
		if err == nil {
			_ = resp.Body.Close()
		}
	}()

	select {
	case code := <-codeCh:
		if code != "auth-code-123" {
			t.Errorf("expected code=auth-code-123, got %s", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for code")
	}
}

func TestSaveToken_Success(t *testing.T) {
	tmpDir := t.TempDir()

	origRootDir := *pkg.RootDir
	origRoot := pkg.Root
	defer func() {
		pkg.RootDir = &origRootDir
		pkg.Root = origRoot
	}()

	pkg.RootDir = &tmpDir
	root, err := os.OpenRoot(tmpDir)
	if err != nil {
		t.Fatalf("failed to open root: %v", err)
	}
	pkg.Root = root

	s := NewY2BService().(*svc)
	s.tokenFile = "token.json"

	tok := &oauth2.Token{AccessToken: "test-token"}
	err = s.saveToken(tok)
	if err != nil {
		t.Fatalf("saveToken returned error: %v", err)
	}

	data, err := os.ReadFile(tmpDir + "/token.json")
	if err != nil {
		t.Fatalf("failed to read saved token file: %v", err)
	}

	var savedToken oauth2.Token
	if err := json.Unmarshal(data, &savedToken); err != nil {
		t.Fatalf("failed to unmarshal saved token: %v", err)
	}
	if savedToken.AccessToken != "test-token" {
		t.Errorf("expected AccessToken=test-token, got %s", savedToken.AccessToken)
	}
}

func TestGetConfig_Scopes(t *testing.T) {
	s := NewY2BService(
		WithCredential(validCredentialJSON("http://localhost"), os.DirFS(".")),
	).(*svc)

	config, err := s.getConfig()
	if err != nil {
		t.Fatalf("getConfig returned error: %v", err)
	}

	if len(config.Scopes) != 3 {
		t.Fatalf("expected 3 scopes, got %d", len(config.Scopes))
	}

	expectedScopes := []string{
		youtube.YoutubeScope,
		youtube.YoutubeForceSslScope,
		youtube.YoutubeChannelMembershipsCreatorScope,
	}
	for i, expected := range expectedScopes {
		if config.Scopes[i] != expected {
			t.Errorf("scope[%d] = %q, want %q", i, config.Scopes[i], expected)
		}
	}
}

func TestGetCodeFromPrompt_ReadError(t *testing.T) {
	errReader := iotest.ErrReader(fmt.Errorf("read error"))
	var out bytes.Buffer

	s := NewY2BService(WithIO(errReader, &out)).(*svc)

	_, err := s.getCodeFromPrompt(
		"http://example.com/auth", "http://localhost:8216",
	)
	if err == nil {
		t.Fatalf("expected error from getCodeFromPrompt, got nil")
	}
	if !strings.Contains(err.Error(), "failed to read prompt") {
		t.Errorf(
			"expected error to contain %q, got %q", "failed to read prompt",
			err.Error(),
		)
	}
}

func TestGetTokenFromWeb_PromptFallbackSuccess(t *testing.T) {
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"web-token","token_type":"Bearer","expires_in":3600}`)
	}))
	defer tokenServer.Close()

	s := NewY2BService(
		WithCredential(validCredentialJSONWithTokenURL("http://127.0.0.1:0", tokenServer.URL), os.DirFS(".")),
		WithIO(strings.NewReader("manual-code\n"), io.Discard),
	).(*svc)
	s.openURLFunc = func(string) error { return errors.New("browser unavailable") }

	config, err := s.getConfig()
	if err != nil {
		t.Fatalf("getConfig() error = %v", err)
	}
	token, err := s.getTokenFromWeb(config, "http://example.com/auth", "verifier")
	if err != nil {
		t.Fatalf("getTokenFromWeb() error = %v", err)
	}
	if token.AccessToken != "web-token" {
		t.Errorf("AccessToken = %q, want web-token", token.AccessToken)
	}
}

func TestGetTokenFromWeb_ExchangeError(t *testing.T) {
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "bad token", http.StatusBadRequest)
	}))
	defer tokenServer.Close()

	s := NewY2BService(
		WithCredential(validCredentialJSONWithTokenURL("http://127.0.0.1:0", tokenServer.URL), os.DirFS(".")),
		WithIO(strings.NewReader("manual-code\n"), io.Discard),
	).(*svc)
	s.openURLFunc = func(string) error { return errors.New("browser unavailable") }

	config, err := s.getConfig()
	if err != nil {
		t.Fatalf("getConfig() error = %v", err)
	}
	_, err = s.getTokenFromWeb(config, "http://example.com/auth", "verifier")
	if err == nil || !strings.Contains(err.Error(), exchangeFailed) {
		t.Fatalf("getTokenFromWeb() error = %v, want %q", err, exchangeFailed)
	}
}

func TestGetTokenFromWeb_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	s := NewY2BService(
		WithContext(ctx),
		WithCallbackTimeout(time.Hour),
	).(*svc)

	_, err := s.waitForCode(make(chan string))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("waitForCode() error = %v, want context.Canceled", err)
	}
}

func TestWaitForCode_Timeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := NewY2BService(WithCallbackTimeout(2 * time.Minute)).(*svc)

		_, err := s.waitForCode(make(chan string))
		if err == nil || !strings.Contains(err.Error(), "authorization timed out after 2m0s") {
			t.Fatalf(
				"waitForCode() error = %v, want authorization timed out after 2m0s", err,
			)
		}
	})
}
