# Jiyu Xiaohongshu Read-only MCP

A private, read-only fork of [`xpzouying/xiaohongshu-mcp`](https://github.com/xpzouying/xiaohongshu-mcp) for browsing Xiaohongshu from ChatGPT.

The MCP server exposes exactly six tools:

- `check_login_status`
- `get_login_qrcode`
- `list_feeds`
- `search_feeds`
- `get_feed_detail`
- `user_profile`

Publishing, commenting, replying, liking, favoriting, notification actions, cookie deletion, and the legacy `/api/v1` HTTP API are not exposed. Regression tests pin the exact tool allowlist and verify that legacy write routes return 404.

“Read-only” means the service does not actively publish or interact with content. Logging in and viewing pages may still affect Xiaohongshu view counts, browsing history, or recommendations.

## Railway

1. Deploy this repository with its `Dockerfile`.
2. Mount a persistent volume at `/app/data`.
3. Set `MCP_TOKEN` to a random secret of at least 32 characters.
4. Keep one replica. `PORT` and `RAILWAY_PUBLIC_DOMAIN` are detected automatically.
5. Optionally set `XHS_PROXY` if Railway's egress cannot reliably reach Xiaohongshu.

The ChatGPT MCP endpoint is:

```text
https://<railway-domain>/mcp
```

Choose OAuth when creating the custom MCP app, enter `MCP_TOKEN` only on this service's authorization page, and verify that the scan returns exactly six tools. Then call `get_login_qrcode` and scan it with the Xiaohongshu mobile app.

## Test

Go 1.24 or newer is required:

```bash
go test ./...
```

This fork retains the upstream MIT License.
