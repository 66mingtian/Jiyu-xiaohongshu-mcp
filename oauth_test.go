package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testOwnerToken = "test-owner-token-which-is-longer-than-32-chars"

func newOAuthTestServer(t *testing.T, oauth *singleUserOAuth) *httptest.Server {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	oauth.registerRoutes(router)
	return httptest.NewServer(router)
}

func registerOAuthClient(t *testing.T, origin, redirectURI string) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"client_name":   "ChatGPT",
		"redirect_uris": []string{redirectURI},
	})
	require.NoError(t, err)
	response, err := http.Post(origin+"/register", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusCreated, response.StatusCode)
	var registration struct {
		ClientID string `json:"client_id"`
	}
	require.NoError(t, json.NewDecoder(response.Body).Decode(&registration))
	require.NotEmpty(t, registration.ClientID)
	return registration.ClientID
}

func TestOAuthAuthorizationCodeFlow(t *testing.T) {
	oauth, err := newSingleUserOAuth(testOwnerToken, "https://readonly.example.com")
	require.NoError(t, err)
	server := newOAuthTestServer(t, oauth)
	defer server.Close()

	redirectURI := "https://chatgpt.com/oauth/callback"
	clientID := registerOAuthClient(t, server.URL, redirectURI)
	verifier := "a-secure-pkce-verifier-with-enough-entropy-123456"
	challengeBytes := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(challengeBytes[:])

	authorizeURL, err := url.Parse(server.URL + "/authorize")
	require.NoError(t, err)
	query := authorizeURL.Query()
	query.Set("response_type", "code")
	query.Set("client_id", clientID)
	query.Set("redirect_uri", redirectURI)
	query.Set("code_challenge", challenge)
	query.Set("code_challenge_method", "S256")
	query.Set("resource", "https://readonly.example.com/mcp")
	query.Set("scope", "mcp")
	query.Set("state", "test-state")
	authorizeURL.RawQuery = query.Encode()

	consent, err := http.Get(authorizeURL.String())
	require.NoError(t, err)
	defer consent.Body.Close()
	require.Equal(t, http.StatusOK, consent.StatusCode)
	htmlBody, err := io.ReadAll(consent.Body)
	require.NoError(t, err)
	match := regexp.MustCompile(`name="approval_id" value="([^"]+)"`).FindSubmatch(htmlBody)
	require.Len(t, match, 2)

	form := url.Values{"approval_id": {string(match[1])}, "password": {testOwnerToken}}
	request, err := http.NewRequest(http.MethodPost, server.URL+"/authorize", bytes.NewBufferString(form.Encode()))
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	approval, err := client.Do(request)
	require.NoError(t, err)
	defer approval.Body.Close()
	require.Equal(t, http.StatusFound, approval.StatusCode)
	callback, err := url.Parse(approval.Header.Get("Location"))
	require.NoError(t, err)
	assert.Equal(t, redirectURI, callback.Scheme+"://"+callback.Host+callback.Path)
	assert.Equal(t, "test-state", callback.Query().Get("state"))
	code := callback.Query().Get("code")
	require.NotEmpty(t, code)

	tokenForm := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"client_id":     {clientID},
		"redirect_uri":  {redirectURI},
		"code_verifier": {verifier},
	}
	exchange, err := http.Post(server.URL+"/token", "application/x-www-form-urlencoded", bytes.NewBufferString(tokenForm.Encode()))
	require.NoError(t, err)
	defer exchange.Body.Close()
	require.Equal(t, http.StatusOK, exchange.StatusCode)
	var token map[string]any
	require.NoError(t, json.NewDecoder(exchange.Body).Decode(&token))
	assert.Equal(t, testOwnerToken, token["access_token"])
	assert.Equal(t, "Bearer", token["token_type"])
	assert.Equal(t, "mcp", token["scope"])

	replay, err := http.Post(server.URL+"/token", "application/x-www-form-urlencoded", bytes.NewBufferString(tokenForm.Encode()))
	require.NoError(t, err)
	defer replay.Body.Close()
	assert.Equal(t, http.StatusBadRequest, replay.StatusCode)
}

func TestOAuthRegistrationRejectsNonOpenAIRedirects(t *testing.T) {
	oauth, err := newSingleUserOAuth(testOwnerToken, "https://readonly.example.com")
	require.NoError(t, err)
	server := newOAuthTestServer(t, oauth)
	defer server.Close()

	body := bytes.NewBufferString(`{"redirect_uris":["https://attacker.example/callback"]}`)
	response, err := http.Post(server.URL+"/register", "application/json", body)
	require.NoError(t, err)
	defer response.Body.Close()
	assert.Equal(t, http.StatusBadRequest, response.StatusCode)
}

func TestOAuthMetadataMatchesMCPResource(t *testing.T) {
	oauth, err := newSingleUserOAuth(testOwnerToken, "https://readonly.example.com/")
	require.NoError(t, err)
	server := newOAuthTestServer(t, oauth)
	defer server.Close()

	response, err := http.Get(server.URL + "/.well-known/oauth-protected-resource/mcp")
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusOK, response.StatusCode)
	var metadata struct {
		Resource             string   `json:"resource"`
		AuthorizationServers []string `json:"authorization_servers"`
		Scopes               []string `json:"scopes_supported"`
	}
	require.NoError(t, json.NewDecoder(response.Body).Decode(&metadata))
	assert.Equal(t, "https://readonly.example.com/mcp", metadata.Resource)
	assert.Equal(t, []string{"https://readonly.example.com"}, metadata.AuthorizationServers)
	assert.Equal(t, []string{"mcp"}, metadata.Scopes)
}
