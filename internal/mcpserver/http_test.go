package mcpserver

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"mcpcloud/internal/config"
)

func TestHTTPBearerHostOriginGate(t *testing.T) {
	const token = "test-token"

	cfg := config.Default()
	cfg.Server.Listen = "127.0.0.1:8080"
	cfg.Server.AllowedHosts = []string{"mcp.internal.test:8443"}
	cfg.Server.AllowedOrigins = []string{"https://console.example"}

	protection := http.NewCrossOriginProtection()
	for _, origin := range cfg.Server.AllowedOrigins {
		if err := protection.AddTrustedOrigin(origin); err != nil {
			t.Fatalf("AddTrustedOrigin(%q): %v", origin, err)
		}
	}

	tests := []struct {
		name          string
		authorization string
		host          string
		origin        string
		wantStatus    int
		wantNext      bool
		wantChallenge string
	}{
		{
			name:          "missing bearer is unauthorized before host checks",
			host:          "evil.example:8080",
			wantStatus:    http.StatusUnauthorized,
			wantChallenge: `Bearer realm="mcpcloud"`,
		},
		{
			name:          "wrong bearer is unauthorized",
			authorization: "Bearer wrong-token",
			host:          "127.0.0.1:8080",
			wantStatus:    http.StatusUnauthorized,
			wantChallenge: `Bearer realm="mcpcloud"`,
		},
		{
			name:          "valid bearer with disallowed host is forbidden",
			authorization: "Bearer " + token,
			host:          "evil.example:8080",
			wantStatus:    http.StatusForbidden,
		},
		{
			name:          "trusted origin cannot bypass disallowed host",
			authorization: "Bearer " + token,
			host:          "evil.example:8080",
			origin:        "https://console.example",
			wantStatus:    http.StatusForbidden,
		},
		{
			name:          "valid bearer and allowed host with untrusted origin is forbidden",
			authorization: "Bearer " + token,
			host:          "127.0.0.1:8080",
			origin:        "https://evil.example",
			wantStatus:    http.StatusForbidden,
		},
		{
			name:          "configured host is allowed without origin",
			authorization: "Bearer " + token,
			host:          "mcp.internal.test:8443",
			wantStatus:    http.StatusNoContent,
			wantNext:      true,
		},
		{
			name:          "trusted origin is allowed on loopback host",
			authorization: "Bearer " + token,
			host:          "127.0.0.1:8080",
			origin:        "https://console.example",
			wantStatus:    http.StatusNoContent,
			wantNext:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nextCalled := false
			next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				nextCalled = true
				w.WriteHeader(http.StatusNoContent)
			})
			handler := bearer(token, hostGuard(cfg, protection.Handler(next)))

			req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/mcp", nil)
			req.Host = tt.host
			if tt.authorization != "" {
				req.Header.Set("Authorization", tt.authorization)
			}
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}

			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, req)

			if recorder.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body = %q", recorder.Code, tt.wantStatus, recorder.Body.String())
			}
			if nextCalled != tt.wantNext {
				t.Fatalf("next called = %t, want %t", nextCalled, tt.wantNext)
			}
			if got := recorder.Header().Get("WWW-Authenticate"); got != tt.wantChallenge {
				t.Fatalf("WWW-Authenticate = %q, want %q", got, tt.wantChallenge)
			}
		})
	}
}
