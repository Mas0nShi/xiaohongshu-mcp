package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestBearerAuthMiddlewareDisabled(t *testing.T) {
	t.Setenv("MCP_BEARER_TOKEN", "")
	t.Setenv("XHS_MCP_BEARER_TOKEN", "")

	router := newAuthTestRouter()
	resp := performAuthTestRequest(router, "")

	if resp.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, resp.Code)
	}
}

func TestBearerAuthMiddlewareEnabled(t *testing.T) {
	t.Setenv("MCP_BEARER_TOKEN", "secret-token")
	t.Setenv("XHS_MCP_BEARER_TOKEN", "")

	router := newAuthTestRouter()

	tests := []struct {
		name        string
		authHeader  string
		wantStatus  int
		wantWWWAuth bool
	}{
		{
			name:        "missing authorization header",
			wantStatus:  http.StatusUnauthorized,
			wantWWWAuth: true,
		},
		{
			name:        "wrong bearer token",
			authHeader:  "Bearer wrong-token",
			wantStatus:  http.StatusUnauthorized,
			wantWWWAuth: true,
		},
		{
			name:       "valid bearer token",
			authHeader: "Bearer secret-token",
			wantStatus: http.StatusOK,
		},
		{
			name:       "case insensitive bearer scheme",
			authHeader: "bearer secret-token",
			wantStatus: http.StatusOK,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp := performAuthTestRequest(router, tc.authHeader)
			if resp.Code != tc.wantStatus {
				t.Fatalf("expected status %d, got %d", tc.wantStatus, resp.Code)
			}
			if tc.wantWWWAuth && resp.Header().Get("WWW-Authenticate") == "" {
				t.Fatal("expected WWW-Authenticate header")
			}
		})
	}
}

func newAuthTestRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	router.Use(bearerAuthMiddleware())
	router.GET("/mcp", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	return router
}

func performAuthTestRequest(router http.Handler, authHeader string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}

	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	return resp
}
