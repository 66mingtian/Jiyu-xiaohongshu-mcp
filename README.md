# 季遇的小红书只读 MCP

这是基于 [`xpzouying/xiaohongshu-mcp`](https://github.com/xpzouying/xiaohongshu-mcp) 的私人只读 fork，用于让 ChatGPT 登录、浏览和搜索小红书。

目标不是自动运营账号，而是给季遇一扇“出去逛”的门。

## 能做什么

MCP 只注册以下 6 个工具：

| 工具 | 用途 |
| --- | --- |
| `check_login_status` | 检查当前登录状态 |
| `get_login_qrcode` | 获取登录二维码 |
| `list_feeds` | 读取首页推荐 |
| `search_feeds` | 搜索笔记 |
| `get_feed_detail` | 读取笔记详情与评论 |
| `user_profile` | 读取用户公开主页 |

以下能力不会注册为 MCP 工具，也没有可调用的旧 `/api/v1` HTTP 路由：

- 发图文或视频
- 发表评论或回复
- 点赞、取消点赞
- 收藏、取消收藏
- 操作通知
- 删除登录 cookies

回归测试会检查 `/tools/list` 精确等于上述白名单，并检查旧写路由均不可达。

> “只读”指不会主动执行发帖、评论、点赞、收藏等账号写操作。登录和浏览仍可能被小红书计入访问、浏览历史并影响推荐。

## Railway 部署

1. 从本仓库创建 Railway 服务，构建方式选择仓库内的 `Dockerfile`。
2. 添加一个 Volume，挂载到 `/app/data`，用于持久化登录 cookies 与浏览器指纹 seed。
3. 设置变量：

   - `MCP_TOKEN`：至少 32 个随机字符；这是 ChatGPT OAuth 授权页要求输入的私钥。
   - `XHS_PROXY`：可选。仅在 Railway 出口访问小红书不稳定时设置。

   Railway 自动提供 `PORT` 与 `RAILWAY_PUBLIC_DOMAIN`；通常不需要手工设置 `PUBLIC_URL`。容器默认把 cookies 写到 `/app/data/cookies.json`。

4. 建议保持单副本。扫码会启动一个短暂浏览器会话，同一账号的 cookies 也只应由一个实例持有。
5. 生成公网域名，健康检查应返回 `service: jiyu-xiaohongshu-readonly` 和 `tools: 6`。

## 连接 ChatGPT

请在 ChatGPT 网页版完成自定义 MCP 配置：

1. 创建新的自定义 App/MCP，不要复用旧工具快照。
2. Endpoint 填：

   ```text
   https://<railway-domain>/mcp
   ```

3. 选择 OAuth，点击 `Scan Tools`。
4. 在本服务的授权页输入 Railway 中设置的 `MCP_TOKEN`。
5. 确认只扫描到上面的 6 个工具，再创建/发布 App。
6. 在 ChatGPT 中调用 `get_login_qrcode`，用小红书 App 扫码；随后调用 `check_login_status`。

小红书同一账号不宜同时登录多个网页端，否则 MCP 的登录可能被挤掉；手机 App 可以继续使用。

## 本地验证

需要 Go 1.24 或更高版本：

```bash
go test ./...
MCP_TOKEN='replace-with-at-least-32-random-characters' go run .
```

本地 MCP 地址为 `http://localhost:18060/mcp`。生产环境必须设置随机 `MCP_TOKEN`，程序不会在缺少安全 token 时启动。

## 上游与许可

浏览器自动化、登录和内容读取能力来自 `xpzouying/xiaohongshu-mcp`。本 fork 保留原项目的 MIT License，并只缩小对外能力边界、加入 ChatGPT OAuth 与 Railway 部署适配。
