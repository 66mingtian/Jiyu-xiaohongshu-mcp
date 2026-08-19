package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type listedTool struct {
	Name        string `json:"name"`
	Annotations struct {
		ReadOnlyHint    bool  `json:"readOnlyHint"`
		DestructiveHint *bool `json:"destructiveHint"`
	} `json:"annotations"`
}

func listMCPTools(t *testing.T, router http.Handler) []listedTool {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	router.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	var response struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
		Result struct {
			Tools []listedTool `json:"tools"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.Nil(t, response.Error)
	return response.Result.Tools
}

func TestMCPStatelessSinglePost(t *testing.T) {
	router := setupRoutes(NewAppServer(NewXiaohongshuService(), ""))
	assert.NotEmpty(t, listMCPTools(t, router))
}

func TestMCPExposesExactReadOnlyWhitelist(t *testing.T) {
	router := setupRoutes(NewAppServer(NewXiaohongshuService(), ""))
	tools := listMCPTools(t, router)

	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
		assert.True(t, tool.Annotations.ReadOnlyHint, "%s 必须标记为只读", tool.Name)
		require.NotNil(t, tool.Annotations.DestructiveHint, "%s 必须显式标记为非破坏性", tool.Name)
		assert.False(t, *tool.Annotations.DestructiveHint, "%s 不得标记为破坏性", tool.Name)
	}

	want := []string{
		"check_login_status",
		"get_login_qrcode",
		"list_feeds",
		"search_feeds",
		"get_feed_detail",
		"user_profile",
	}
	sort.Strings(names)
	sort.Strings(want)
	assert.Equal(t, want, names)
}

func TestLegacyHTTPAPIIsNotExposed(t *testing.T) {
	router := setupRoutes(NewAppServer(NewXiaohongshuService(), ""))

	for _, route := range router.Routes() {
		assert.False(t, strings.HasPrefix(route.Path, "/api/"), "不应暴露旧 HTTP API: %s %s", route.Method, route.Path)
	}

	for _, path := range []string{
		"/api/v1/publish",
		"/api/v1/publish_video",
		"/api/v1/feeds/comment",
		"/api/v1/feeds/comment/reply",
		"/api/v1/feeds/like",
		"/api/v1/feeds/favorite",
		"/api/v1/notifications/reply",
		"/api/v1/notifications/like",
		"/api/v1/login/cookies",
	} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, path, nil)
		router.ServeHTTP(recorder, request)
		assert.Equal(t, http.StatusNotFound, recorder.Code, "%s 必须不可达", path)
	}
}

func TestProtectedRoutesRequireBearerToken(t *testing.T) {
	router := setupRoutes(NewAppServer(NewXiaohongshuService(), "secret-token", "https://readonly.example.com"))

	tests := []struct {
		name       string
		method     string
		path       string
		wantStatus int
	}{
		{name: "health remains public", method: http.MethodGet, path: "/health", wantStatus: http.StatusOK},
		{name: "OAuth metadata remains public", method: http.MethodGet, path: "/.well-known/oauth-protected-resource/mcp", wantStatus: http.StatusOK},
		{name: "MCP requires token", method: http.MethodPost, path: "/mcp", wantStatus: http.StatusUnauthorized},
		{name: "MCP child path requires token", method: http.MethodPost, path: "/mcp/child", wantStatus: http.StatusUnauthorized},
		{name: "CORS preflight remains public", method: http.MethodOptions, path: "/mcp", wantStatus: http.StatusNoContent},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(tt.method, tt.path, nil)
			router.ServeHTTP(recorder, request)
			assert.Equal(t, tt.wantStatus, recorder.Code)
		})
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	router.ServeHTTP(recorder, request)
	assert.Equal(t,
		"Bearer resource_metadata=https://readonly.example.com/.well-known/oauth-protected-resource/mcp",
		recorder.Header().Get("WWW-Authenticate"))
}

func TestMCPAcceptsConfiguredBearerToken(t *testing.T) {
	router := setupRoutes(NewAppServer(NewXiaohongshuService(), "secret-token", "https://readonly.example.com"))
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	request.Header.Set("Authorization", "Bearer secret-token")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")

	router.ServeHTTP(recorder, request)
	assert.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
}
