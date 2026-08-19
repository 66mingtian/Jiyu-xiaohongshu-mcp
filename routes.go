package main

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// setupRoutes 设置路由配置
func setupRoutes(appServer *AppServer) *gin.Engine {
	// 设置 Gin 模式
	gin.SetMode(gin.ReleaseMode)

	router := gin.New()
	_ = router.SetTrustedProxies(nil)
	router.Use(gin.Logger())
	router.Use(gin.Recovery())

	// 添加中间件
	router.Use(errorHandlingMiddleware())
	router.Use(corsMiddleware())

	// 健康检查
	router.GET("/health", healthHandler)
	if appServer.oauth != nil {
		appServer.oauth.registerRoutes(router)
	}

	// MCP 端点 - 使用官方 SDK 的 Streamable HTTP Handler
	mcpHandler := mcp.NewStreamableHTTPHandler(
		func(r *http.Request) *mcp.Server {
			return appServer.mcpServer
		},
		&mcp.StreamableHTTPOptions{
			JSONResponse: true, // 支持 JSON 响应
			// 换取客户端可跳过 initialize 握手直接调工具。代价是服务端无法反向
			// 请求客户端（sampling / elicitation / roots），眼下一处都没用到；
			// 要用得先摘掉这行。
			Stateless: true,
		},
	)
	protected := router.Group("")
	resourceMetadataURL := ""
	if appServer.oauth != nil {
		resourceMetadataURL = appServer.oauth.resourceMetadata
	}
	protected.Use(authMiddleware(appServer.authToken, resourceMetadataURL))

	protected.Any("/mcp", gin.WrapH(mcpHandler))
	protected.Any("/mcp/*path", gin.WrapH(mcpHandler))

	return router
}
