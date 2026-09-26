package ai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// withMetaURLs points the Meta OAuth flow at a test server.
func withMetaURLs(t *testing.T, base string, run func()) {
	t.Helper()
	originalAuth, originalToken, originalMint := metaDeviceAuthorizationURL, metaDeviceTokenURL, metaAPIKeyMintURL
	metaDeviceAuthorizationURL = base + "/oidc/device/authorization/"
	metaDeviceTokenURL = base + "/oidc/device/token/"
	metaAPIKeyMintURL = base + "/muse-code/key"
	defer func() {
		metaDeviceAuthorizationURL = originalAuth
		metaDeviceTokenURL = originalToken
		metaAPIKeyMintURL = originalMint
	}()
	run()
}

// The Meta login runs the RFC 8628 device flow, then exchanges the identity
// token for a Model API key and stores the identity token as the refresh token
// (upstream loginMeta).
func TestMetaOAuthLogin(t *testing.T) {
	fastDeviceCodeFlows(t)
	var tokenCalls int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/oidc/device/authorization/":
			body := readForm(t, request)
			if body.Get("client_id") != metaOAuthClientID {
				t.Errorf("client_id = %q", body.Get("client_id"))
			}
			_, _ = writer.Write([]byte(`{"device_code":"device-1","user_code":"CODE-1","verification_uri":"https://meta.example/device","verification_uri_complete":"https://meta.example/device?code=CODE-1","expires_in":600,"interval":1}`))
		case "/oidc/device/token/":
			tokenCalls++
			body := readForm(t, request)
			if body.Get("grant_type") != "urn:ietf:params:oauth:grant-type:device_code" || body.Get("device_code") != "device-1" {
				t.Errorf("token body = %v", body)
			}
			if tokenCalls == 1 {
				writer.WriteHeader(http.StatusBadRequest)
				_, _ = writer.Write([]byte(`{"error":"authorization_pending"}`))
				return
			}
			_, _ = writer.Write([]byte(`{"access_token":"identity-token"}`))
		case "/muse-code/key":
			if auth := request.Header.Get("Authorization"); auth != "Bearer identity-token" {
				t.Errorf("authorization = %q", auth)
			}
			if version := request.Header.Get("x-api-version"); version != "1.0.0" {
				t.Errorf("x-api-version = %q", version)
			}
			_, _ = writer.Write([]byte(`{"api_key":"muse-key"}`))
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	withMetaURLs(t, server.URL, func() {
		var notified []AuthEvent
		before := time.Now().UnixMilli()
		credential, err := MetaOAuth().Login(&AuthInteraction{
			Ctx:    context.Background(),
			Notify: func(event AuthEvent) { notified = append(notified, event) },
		})
		if err != nil {
			t.Fatal(err)
		}
		if credential.Access != "muse-key" || credential.Refresh != "identity-token" {
			t.Fatalf("credential = %+v", credential)
		}
		if credential.Expires < before+metaAPIKeyLifetimeMS {
			t.Fatalf("expires = %d; want about now+24h", credential.Expires)
		}
		if len(notified) != 2 {
			t.Fatalf("notifications = %+v", notified)
		}
		if notified[0].Type != AuthEventDeviceCode || notified[0].UserCode != "CODE-1" ||
			notified[0].VerificationURI != "https://meta.example/device?code=CODE-1" || notified[0].ExpiresSeconds != 600 {
			t.Fatalf("device notification = %+v", notified[0])
		}
		if notified[1].Type != AuthEventProgress || notified[1].Message != "Enabling Meta Model API access..." {
			t.Fatalf("progress notification = %+v", notified[1])
		}
	})
}

// Refresh re-mints the Model API key from the stored identity token (upstream
// metaOAuth.refresh).
func TestMetaOAuthRefreshRemintsTheKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/muse-code/key" {
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		if auth := request.Header.Get("Authorization"); auth != "Bearer identity-token" {
			t.Errorf("authorization = %q", auth)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"api_key":"fresh-key"}`))
	}))
	defer server.Close()

	withMetaURLs(t, server.URL, func() {
		credential, err := MetaOAuth().Refresh(&OAuthCredential{OAuthCredentials: OAuthCredentials{Refresh: "identity-token"}}, context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if credential.Access != "fresh-key" || credential.Refresh != "identity-token" {
			t.Fatalf("credential = %+v", credential)
		}
	})
}

// A 401/403 from the mint endpoint means the identity token is dead; the
// message tells the user to sign in again (upstream mintApiKey).
func TestMetaOAuthMintExpiredSession(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = writer.Write([]byte(`{"detail":"token expired"}`))
	}))
	defer server.Close()

	withMetaURLs(t, server.URL, func() {
		_, err := MetaOAuth().Refresh(&OAuthCredential{OAuthCredentials: OAuthCredentials{Refresh: "stale"}}, context.Background())
		if err == nil || !strings.Contains(err.Error(), "Run `/login meta` to sign in again") {
			t.Fatalf("err = %v", err)
		}
	})
}
