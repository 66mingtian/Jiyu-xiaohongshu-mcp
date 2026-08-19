package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/sirupsen/logrus"
	"github.com/xpzouying/xiaohongshu-mcp/browser"
	"github.com/xpzouying/xiaohongshu-mcp/configs"
	"github.com/xpzouying/xiaohongshu-mcp/cookies"
)

// version 构建版本号，发布时通过 -ldflags "-X main.version=vX.Y.Z" 注入。
var version = "dev"

func main() {
	var (
		headless bool
		port     string
		token    string
	)
	defaultPort := ":18060"
	if railwayPort := os.Getenv("PORT"); railwayPort != "" {
		defaultPort = ":" + strings.TrimPrefix(railwayPort, ":")
	}
	flag.BoolVar(&headless, "headless", true, "是否无头模式")
	flag.StringVar(&port, "port", defaultPort, "端口")
	flag.StringVar(&token, "token", "", "鉴权 Token，留空则读取 MCP_TOKEN（兼容 AUTH_TOKEN）")
	flag.Parse()
	if token == "" {
		token = os.Getenv("MCP_TOKEN")
	}
	if token == "" {
		token = os.Getenv("AUTH_TOKEN")
	}
	if len(token) < 32 {
		logrus.Fatal("MCP_TOKEN must be a random secret of at least 32 characters")
	}
	publicOrigin := resolvePublicOrigin(port)

	logrus.Infof("xiaohongshu-mcp version: %s", version)

	// 只用内置浏览器。启动时就备好，缺它直接退出，不拖到第一个请求才失败。
	binPath, err := browser.EnsureBrowser()
	if err != nil {
		logrus.Fatalf("%v", err)
	}
	logrus.Infof("using browser binary: %s", binPath)

	configs.InitHeadless(headless)
	// 入口层解析出 seed 和代理，经 configs 透传给浏览器工厂。
	// seed 取值：环境变量 > 会话文件 > 新生成并写回，保证同一账号每次启动一致。
	configs.SetFingerprintSeed(configs.ResolveFingerprintSeed(
		cookies.NewLoadCookie(cookies.GetCookiesFilePath())))
	configs.SetProxy(configs.ProxyFromEnv())

	// 初始化服务
	xiaohongshuService := NewXiaohongshuService()

	// 创建并启动应用服务器
	appServer := NewAppServer(xiaohongshuService, token, publicOrigin)
	if err := appServer.Start(port); err != nil {
		logrus.Fatalf("failed to run server: %v", err)
	}
}

func resolvePublicOrigin(listenAddress string) string {
	if configured := strings.TrimRight(os.Getenv("PUBLIC_URL"), "/"); configured != "" {
		return configured
	}
	if domain := strings.TrimSpace(os.Getenv("RAILWAY_PUBLIC_DOMAIN")); domain != "" {
		return "https://" + strings.TrimRight(domain, "/")
	}
	port := strings.TrimPrefix(listenAddress, ":")
	if parsed, err := strconv.Atoi(port); err != nil || parsed < 1 || parsed > 65535 {
		panic(fmt.Sprintf("invalid listen port %q", listenAddress))
	}
	return "http://localhost:" + port
}
