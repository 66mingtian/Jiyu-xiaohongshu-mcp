package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	approvalTTL  = 30 * time.Minute
	codeTTL      = 10 * time.Minute
	attemptTTL   = 10 * time.Minute
	maxAttempts  = 8
	maxOAuthBody = 32 << 10
)

type oauthAttempt struct {
	Count   int
	ResetAt time.Time
}

type oauthPayload struct {
	Type          string   `json:"type"`
	ExpiresAt     int64    `json:"expires_at,omitempty"`
	ClientID      string   `json:"client_id,omitempty"`
	ClientName    string   `json:"client_name,omitempty"`
	RedirectURIs  []string `json:"redirect_uris,omitempty"`
	RedirectURI   string   `json:"redirect_uri,omitempty"`
	CodeChallenge string   `json:"code_challenge,omitempty"`
	State         string   `json:"state,omitempty"`
	Scope         string   `json:"scope,omitempty"`
	Resource      string   `json:"resource,omitempty"`
	Nonce         string   `json:"nonce,omitempty"`
}

type singleUserOAuth struct {
	ownerToken        string
	issuer            string
	resource          string
	resourceMetadata  string
	failedAttempts    map[string]oauthAttempt
	consumedCodeUntil map[string]time.Time
	mu                sync.Mutex
}

func newSingleUserOAuth(ownerToken, publicOrigin string) (*singleUserOAuth, error) {
	issuerURL, err := url.Parse(publicOrigin)
	if err != nil || issuerURL.Host == "" || (issuerURL.Scheme != "https" && issuerURL.Scheme != "http") {
		return nil, fmt.Errorf("invalid PUBLIC_URL %q", publicOrigin)
	}
	issuerURL.RawQuery = ""
	issuerURL.Fragment = ""
	issuerURL.Path = strings.TrimRight(issuerURL.Path, "/")
	issuer := strings.TrimRight(issuerURL.String(), "/")
	resource := issuer + "/mcp"

	return &singleUserOAuth{
		ownerToken:        ownerToken,
		issuer:            issuer,
		resource:          resource,
		resourceMetadata:  issuer + "/.well-known/oauth-protected-resource/mcp",
		failedAttempts:    make(map[string]oauthAttempt),
		consumedCodeUntil: make(map[string]time.Time),
	}, nil
}

func (o *singleUserOAuth) registerRoutes(router *gin.Engine) {
	router.GET("/", o.handleRoot)
	router.GET("/.well-known/oauth-protected-resource", o.handleProtectedResourceMetadata)
	router.GET("/.well-known/oauth-protected-resource/mcp", o.handleProtectedResourceMetadata)
	router.GET("/.well-known/oauth-authorization-server", o.handleAuthorizationMetadata)
	router.GET("/.well-known/openid-configuration", o.handleAuthorizationMetadata)
	router.POST("/register", o.handleRegister)
	router.GET("/authorize", o.handleAuthorizeForm)
	router.POST("/authorize", o.handleAuthorizeApproval)
	router.POST("/token", o.handleToken)
}

func (o *singleUserOAuth) handleRoot(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte("<!doctype html><html lang=\"zh-CN\"><meta name=\"viewport\" content=\"width=device-width,initial-scale=1\"><title>季遇的小红书只读 MCP</title><body><h1>季遇的小红书只读 MCP</h1><p>这是私有服务，请通过 ChatGPT OAuth 连接。</p></body></html>"))
}

func (o *singleUserOAuth) handleProtectedResourceMetadata(c *gin.Context) {
	c.Header("Cache-Control", "public, max-age=300")
	c.JSON(http.StatusOK, gin.H{
		"resource":                 o.resource,
		"authorization_servers":    []string{o.issuer},
		"scopes_supported":         []string{"mcp"},
		"bearer_methods_supported": []string{"header"},
	})
}

func (o *singleUserOAuth) handleAuthorizationMetadata(c *gin.Context) {
	c.Header("Cache-Control", "public, max-age=300")
	c.JSON(http.StatusOK, gin.H{
		"issuer":                                o.issuer,
		"authorization_endpoint":                o.issuer + "/authorize",
		"token_endpoint":                        o.issuer + "/token",
		"registration_endpoint":                 o.issuer + "/register",
		"scopes_supported":                      []string{"mcp"},
		"response_types_supported":              []string{"code"},
		"response_modes_supported":              []string{"query"},
		"grant_types_supported":                 []string{"authorization_code"},
		"token_endpoint_auth_methods_supported": []string{"none"},
		"code_challenge_methods_supported":      []string{"S256"},
	})
}

type dynamicClientRegistration struct {
	ClientName   string   `json:"client_name"`
	RedirectURIs []string `json:"redirect_uris"`
}

func (o *singleUserOAuth) handleRegister(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxOAuthBody)
	var req dynamicClientRegistration
	if err := json.NewDecoder(c.Request.Body).Decode(&req); err != nil {
		o.oauthError(c, http.StatusBadRequest, "invalid_client_metadata", "Invalid client registration JSON")
		return
	}
	if len(req.RedirectURIs) == 0 {
		o.oauthError(c, http.StatusBadRequest, "invalid_redirect_uri", "At least one redirect URI is required")
		return
	}

	redirects := make([]string, 0, len(req.RedirectURIs))
	for _, redirect := range req.RedirectURIs {
		if !officialOpenAIURL(redirect) {
			o.oauthError(c, http.StatusBadRequest, "invalid_redirect_uri", "Only HTTPS OpenAI or ChatGPT redirect URIs are accepted")
			return
		}
		if !slices.Contains(redirects, redirect) {
			redirects = append(redirects, redirect)
		}
	}

	clientName := strings.TrimSpace(req.ClientName)
	if clientName == "" {
		clientName = "ChatGPT"
	}
	clientID, err := o.seal(oauthPayload{
		Type:         "client",
		ClientName:   clientName,
		RedirectURIs: redirects,
	})
	if err != nil {
		o.oauthError(c, http.StatusInternalServerError, "server_error", "Could not register client")
		return
	}

	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusCreated, gin.H{
		"client_id":                  clientID,
		"client_id_issued_at":        time.Now().Unix(),
		"client_name":                clientName,
		"redirect_uris":              redirects,
		"token_endpoint_auth_method": "none",
		"grant_types":                []string{"authorization_code"},
		"response_types":             []string{"code"},
	})
}

func (o *singleUserOAuth) handleAuthorizeForm(c *gin.Context) {
	request, err := o.validateAuthorizeRequest(c.Request.URL.Query())
	if err != nil {
		o.oauthError(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	approvalID, err := o.sealWithTTL(request, approvalTTL)
	if err != nil {
		o.oauthError(c, http.StatusInternalServerError, "server_error", "Could not create authorization request")
		return
	}

	c.Header("Cache-Control", "no-store")
	c.Header("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
	c.Header("Referrer-Policy", "no-referrer")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("X-Frame-Options", "DENY")
	body := fmt.Sprintf(`<!doctype html>
<html lang="zh-CN"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>授权季遇的小红书只读 MCP</title><style>
body{margin:0;background:#111018;color:#f7f4ff;font:16px system-ui;min-height:100vh;display:grid;place-items:center}
main{width:min(88vw,420px);background:#1c1927;padding:28px;border-radius:20px;border:1px solid #39324c}h1{font-size:24px;margin:0 0 10px}p{color:#bbb4ca;line-height:1.55}label{display:block;margin:22px 0 8px}
input{box-sizing:border-box;width:100%%;padding:14px;border-radius:12px;border:1px solid #514667;background:#0f0e15;color:white;font-size:16px}button{width:100%%;margin-top:18px;padding:14px;border:0;border-radius:12px;background:#d13c72;color:white;font-weight:700;font-size:16px}</style>
<main><h1>授权“小红书只读 MCP”</h1><p>%s 请求连接。它只能登录、浏览、搜索和读取公开内容，不能发帖、评论、点赞或收藏。</p>
<form method="post" action="/authorize"><input type="hidden" name="approval_id" value="%s"><label for="password">MCP_TOKEN</label><input id="password" name="password" type="password" autocomplete="current-password" required autofocus><button type="submit">允许连接</button></form></main></html>`,
		html.EscapeString(request.ClientName), html.EscapeString(approvalID))
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(body))
}

func (o *singleUserOAuth) handleAuthorizeApproval(c *gin.Context) {
	if err := c.Request.ParseForm(); err != nil {
		o.oauthError(c, http.StatusBadRequest, "invalid_request", "Invalid authorization form")
		return
	}
	approval := o.unseal(c.PostForm("approval_id"), "approval")
	if approval == nil {
		o.oauthError(c, http.StatusBadRequest, "invalid_request", "Authorization request expired")
		return
	}

	address := remoteAddress(c.Request)
	if o.tooManyAttempts(address) {
		o.oauthError(c, http.StatusTooManyRequests, "temporarily_unavailable", "Too many failed attempts; try again later")
		return
	}
	if !constantTimeEqual(c.PostForm("password"), o.ownerToken) {
		o.recordFailure(address)
		c.Header("Cache-Control", "no-store")
		c.Data(http.StatusUnauthorized, "text/html; charset=utf-8", []byte("<h1>密钥不正确</h1><p>请返回上一页重试。</p>"))
		return
	}
	o.clearFailures(address)

	code, err := o.sealWithTTL(oauthPayload{
		Type:          "code",
		ClientID:      approval.ClientID,
		RedirectURI:   approval.RedirectURI,
		CodeChallenge: approval.CodeChallenge,
		Scope:         approval.Scope,
		Resource:      approval.Resource,
	}, codeTTL)
	if err != nil {
		o.oauthError(c, http.StatusInternalServerError, "server_error", "Could not create authorization code")
		return
	}
	target, _ := url.Parse(approval.RedirectURI)
	query := target.Query()
	query.Set("code", code)
	if approval.State != "" {
		query.Set("state", approval.State)
	}
	target.RawQuery = query.Encode()
	c.Redirect(http.StatusFound, target.String())
}

func (o *singleUserOAuth) handleToken(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxOAuthBody)
	if err := c.Request.ParseForm(); err != nil {
		o.oauthError(c, http.StatusBadRequest, "invalid_request", "Invalid token request")
		return
	}
	if c.PostForm("grant_type") != "authorization_code" {
		o.oauthError(c, http.StatusBadRequest, "unsupported_grant_type", "Only authorization_code is supported")
		return
	}

	code := c.PostForm("code")
	authorization := o.unseal(code, "code")
	if authorization == nil {
		o.oauthError(c, http.StatusBadRequest, "invalid_grant", "Authorization code is invalid or expired")
		return
	}
	if !constantTimeEqual(c.PostForm("client_id"), authorization.ClientID) ||
		!constantTimeEqual(c.PostForm("redirect_uri"), authorization.RedirectURI) {
		o.oauthError(c, http.StatusBadRequest, "invalid_grant", "Client or redirect URI does not match")
		return
	}
	verifier := c.PostForm("code_verifier")
	challengeBytes := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(challengeBytes[:])
	if verifier == "" || !constantTimeEqual(challenge, authorization.CodeChallenge) {
		o.oauthError(c, http.StatusBadRequest, "invalid_grant", "PKCE verification failed")
		return
	}
	if !o.consumeCode(code, time.UnixMilli(authorization.ExpiresAt)) {
		o.oauthError(c, http.StatusBadRequest, "invalid_grant", "Authorization code was already used")
		return
	}

	c.Header("Cache-Control", "no-store")
	c.Header("Pragma", "no-cache")
	c.JSON(http.StatusOK, gin.H{
		"access_token": o.ownerToken,
		"token_type":   "Bearer",
		"scope":        "mcp",
	})
}

func (o *singleUserOAuth) validateAuthorizeRequest(query url.Values) (oauthPayload, error) {
	if query.Get("response_type") != "code" {
		return oauthPayload{}, errors.New("only response_type=code is supported")
	}
	clientID := query.Get("client_id")
	client := o.unseal(clientID, "client")
	if client == nil {
		return oauthPayload{}, errors.New("unknown OAuth client; dynamic registration is required")
	}
	redirectURI := query.Get("redirect_uri")
	if !slices.Contains(client.RedirectURIs, redirectURI) {
		return oauthPayload{}, errors.New("redirect_uri was not registered")
	}
	codeChallenge := query.Get("code_challenge")
	if codeChallenge == "" || query.Get("code_challenge_method") != "S256" {
		return oauthPayload{}, errors.New("PKCE with S256 is required")
	}
	requestedResource := query.Get("resource")
	if requestedResource != "" && requestedResource != o.resource {
		return oauthPayload{}, errors.New("invalid resource")
	}
	scope := query.Get("scope")
	if scope == "" {
		scope = "mcp"
	}
	if !slices.Contains(strings.Fields(scope), "mcp") {
		return oauthPayload{}, errors.New("the mcp scope is required")
	}

	return oauthPayload{
		Type:          "approval",
		ClientID:      clientID,
		ClientName:    client.ClientName,
		RedirectURI:   redirectURI,
		CodeChallenge: codeChallenge,
		State:         query.Get("state"),
		Scope:         "mcp",
		Resource:      o.resource,
	}, nil
}

func (o *singleUserOAuth) seal(payload oauthPayload) (string, error) {
	return o.sealWithTTL(payload, 0)
}

func (o *singleUserOAuth) sealWithTTL(payload oauthPayload, ttl time.Duration) (string, error) {
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	payload.Nonce = base64.RawURLEncoding.EncodeToString(nonce)
	if ttl > 0 {
		payload.ExpiresAt = time.Now().Add(ttl).UnixMilli()
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	body := base64.RawURLEncoding.EncodeToString(encoded)
	return body + "." + o.sign(body), nil
}

func (o *singleUserOAuth) unseal(token, expectedType string) *oauthPayload {
	parts := strings.Split(token, ".")
	if len(parts) != 2 || !constantTimeEqual(parts[1], o.sign(parts[0])) {
		return nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil
	}
	var payload oauthPayload
	if err := json.Unmarshal(decoded, &payload); err != nil || payload.Type != expectedType {
		return nil
	}
	if payload.ExpiresAt > 0 && time.Now().UnixMilli() >= payload.ExpiresAt {
		return nil
	}
	return &payload
}

func (o *singleUserOAuth) sign(value string) string {
	mac := hmac.New(sha256.New, []byte(o.ownerToken))
	_, _ = mac.Write([]byte(value))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (o *singleUserOAuth) tooManyAttempts(address string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.pruneLocked()
	attempt := o.failedAttempts[address]
	return attempt.Count >= maxAttempts && time.Now().Before(attempt.ResetAt)
}

func (o *singleUserOAuth) recordFailure(address string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.pruneLocked()
	attempt := o.failedAttempts[address]
	if attempt.ResetAt.IsZero() || time.Now().After(attempt.ResetAt) {
		attempt = oauthAttempt{ResetAt: time.Now().Add(attemptTTL)}
	}
	attempt.Count++
	o.failedAttempts[address] = attempt
}

func (o *singleUserOAuth) clearFailures(address string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.failedAttempts, address)
}

func (o *singleUserOAuth) consumeCode(code string, expiresAt time.Time) bool {
	hash := sha256.Sum256([]byte(code))
	key := hex.EncodeToString(hash[:])
	o.mu.Lock()
	defer o.mu.Unlock()
	o.pruneLocked()
	if _, exists := o.consumedCodeUntil[key]; exists {
		return false
	}
	o.consumedCodeUntil[key] = expiresAt
	return true
}

func (o *singleUserOAuth) pruneLocked() {
	now := time.Now()
	for address, attempt := range o.failedAttempts {
		if !now.Before(attempt.ResetAt) {
			delete(o.failedAttempts, address)
		}
	}
	for code, expiry := range o.consumedCodeUntil {
		if !now.Before(expiry) {
			delete(o.consumedCodeUntil, code)
		}
	}
}

func (o *singleUserOAuth) oauthError(c *gin.Context, status int, code, description string) {
	c.Header("Cache-Control", "no-store")
	c.JSON(status, gin.H{"error": code, "error_description": description})
}

func officialOpenAIURL(value string) bool {
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "chatgpt.com" || strings.HasSuffix(host, ".chatgpt.com") ||
		host == "openai.com" || strings.HasSuffix(host, ".openai.com")
}

func constantTimeEqual(left, right string) bool {
	a := []byte(left)
	b := []byte(right)
	return len(a) == len(b) && subtle.ConstantTimeCompare(a, b) == 1
}

func remoteAddress(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}
