package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"runtime/debug"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sirupsen/logrus"
)

// MCP 工具参数结构体定义。这个 fork 只暴露登录与浏览所需的参数；
// 发布、互动、通知等写操作不进入 MCP schema。

type SearchFeedsArgs struct {
	Keyword string       `json:"keyword" jsonschema:"搜索关键词"`
	Filters FilterOption `json:"filters,omitempty" jsonschema:"筛选选项"`
}

type FilterOption struct {
	SortBy      string `json:"sort_by,omitempty" jsonschema:"排序依据: 综合|最新|最多点赞|最多评论|最多收藏,默认为'综合'"`
	NoteType    string `json:"note_type,omitempty" jsonschema:"笔记类型: 不限|视频|图文,默认为'不限'"`
	PublishTime string `json:"publish_time,omitempty" jsonschema:"发布时间: 不限|一天内|一周内|半年内,默认为'不限'"`
	SearchScope string `json:"search_scope,omitempty" jsonschema:"搜索范围: 不限|已看过|未看过|已关注,默认为'不限'"`
	Location    string `json:"location,omitempty" jsonschema:"位置距离: 不限|同城|附近,默认为'不限'"`
}

type FeedDetailArgs struct {
	FeedID           string `json:"feed_id" jsonschema:"小红书笔记ID，从Feed列表获取"`
	XsecToken        string `json:"xsec_token" jsonschema:"访问令牌，从Feed列表的xsecToken字段获取"`
	LoadAllComments  bool   `json:"load_all_comments,omitempty" jsonschema:"是否加载全部评论。false仅返回前10条一级评论（默认），true滚动加载更多评论"`
	Limit            int    `json:"limit,omitempty" jsonschema:"【仅当load_all_comments为true时生效】限制加载的一级评论数量。例如20表示最多加载20条，默认20"`
	ClickMoreReplies bool   `json:"click_more_replies,omitempty" jsonschema:"【仅当load_all_comments为true时生效】是否展开二级回复"`
	ReplyLimit       int    `json:"reply_limit,omitempty" jsonschema:"【仅当click_more_replies为true时生效】跳过回复数过多的评论，默认10"`
	ScrollSpeed      string `json:"scroll_speed,omitempty" jsonschema:"【仅当load_all_comments为true时生效】滚动速度slow慢速、normal正常、fast快速"`
}

type UserProfileArgs struct {
	UserID    string `json:"user_id" jsonschema:"小红书用户ID，从Feed列表获取"`
	XsecToken string `json:"xsec_token" jsonschema:"访问令牌，从Feed列表的xsecToken字段获取"`
	Tab       string `json:"tab,omitempty" jsonschema:"主页 tab: note(笔记,默认)|fav(收藏)|liked(点赞)。收藏和点赞可能被对方设为不公开"`
}

func boolPtr(b bool) *bool { return &b }

func readOnlyAnnotations(title string) *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{
		Title:           title,
		ReadOnlyHint:    true,
		DestructiveHint: boolPtr(false),
		IdempotentHint:  true,
		OpenWorldHint:   boolPtr(true),
	}
}

func loginAnnotations() *mcp.ToolAnnotations {
	annotations := readOnlyAnnotations("Get Login QR Code")
	annotations.IdempotentHint = false
	return annotations
}

func InitMCPServer(appServer *AppServer) *mcp.Server {
	server := mcp.NewServer(
		&mcp.Implementation{
			Name:    "jiyu-xiaohongshu-readonly",
			Version: "0.1.0",
		},
		nil,
	)

	registerTools(server, appServer)
	logrus.Info("read-only MCP server initialized with official SDK")
	return server
}

func withPanicRecovery[T any](
	toolName string,
	handler func(context.Context, *mcp.CallToolRequest, T) (*mcp.CallToolResult, any, error),
) func(context.Context, *mcp.CallToolRequest, T) (*mcp.CallToolResult, any, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, args T) (result *mcp.CallToolResult, resp any, err error) {
		defer func() {
			if r := recover(); r != nil {
				logrus.WithFields(logrus.Fields{
					"tool":  toolName,
					"panic": r,
				}).Error("tool handler panicked")
				logrus.Errorf("stack trace:\n%s", debug.Stack())

				result = &mcp.CallToolResult{
					Content: []mcp.Content{
						&mcp.TextContent{Text: fmt.Sprintf("工具 %s 执行时发生内部错误: %v", toolName, r)},
					},
					IsError: true,
				}
				resp = nil
				err = nil
			}
		}()

		return handler(ctx, req, args)
	}
}

// registerTools 是这个 fork 的能力边界。不要把写工具或具有隐式写副作用的
// 通知工具加回来；回归测试会固定这份精确白名单。
func registerTools(server *mcp.Server, appServer *AppServer) {
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "check_login_status",
			Description: "检查小红书登录状态",
			Annotations: readOnlyAnnotations("Check Login Status"),
		},
		withPanicRecovery("check_login_status", func(ctx context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, any, error) {
			return convertToMCPResult(appServer.handleCheckLoginStatus(ctx)), nil, nil
		}),
	)

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "get_login_qrcode",
			Description: "获取登录二维码（返回图片和超时时间）；只建立登录会话，不执行内容或互动操作",
			Annotations: loginAnnotations(),
		},
		withPanicRecovery("get_login_qrcode", func(ctx context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, any, error) {
			return convertToMCPResult(appServer.handleGetLoginQrcode(ctx)), nil, nil
		}),
	)

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "list_feeds",
			Description: "获取小红书首页推荐列表",
			Annotations: readOnlyAnnotations("List Feeds"),
		},
		withPanicRecovery("list_feeds", func(ctx context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, any, error) {
			return convertToMCPResult(appServer.handleListFeeds(ctx)), nil, nil
		}),
	)

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "search_feeds",
			Description: "按关键词搜索小红书公开内容（需要已登录）",
			Annotations: readOnlyAnnotations("Search Feeds"),
		},
		withPanicRecovery("search_feeds", func(ctx context.Context, _ *mcp.CallToolRequest, args SearchFeedsArgs) (*mcp.CallToolResult, any, error) {
			return convertToMCPResult(appServer.handleSearchFeeds(ctx, args)), nil, nil
		}),
	)

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "get_feed_detail",
			Description: "获取小红书笔记详情、作者信息、互动数据和评论；不会发表评论或执行互动",
			Annotations: readOnlyAnnotations("Get Feed Detail"),
		},
		withPanicRecovery("get_feed_detail", func(ctx context.Context, _ *mcp.CallToolRequest, args FeedDetailArgs) (*mcp.CallToolResult, any, error) {
			argsMap := map[string]interface{}{
				"feed_id":           args.FeedID,
				"xsec_token":        args.XsecToken,
				"load_all_comments": args.LoadAllComments,
			}
			if args.LoadAllComments {
				argsMap["click_more_replies"] = args.ClickMoreReplies
				limit := args.Limit
				if limit <= 0 {
					limit = 20
				}
				argsMap["max_comment_items"] = limit
				replyLimit := args.ReplyLimit
				if replyLimit <= 0 {
					replyLimit = 10
				}
				argsMap["max_replies_threshold"] = replyLimit
				if args.ScrollSpeed != "" {
					argsMap["scroll_speed"] = args.ScrollSpeed
				}
			}
			return convertToMCPResult(appServer.handleGetFeedDetail(ctx, argsMap)), nil, nil
		}),
	)

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "user_profile",
			Description: "获取指定小红书用户的公开主页信息和可见笔记",
			Annotations: readOnlyAnnotations("User Profile"),
		},
		withPanicRecovery("user_profile", func(ctx context.Context, _ *mcp.CallToolRequest, args UserProfileArgs) (*mcp.CallToolResult, any, error) {
			argsMap := map[string]interface{}{
				"user_id":    args.UserID,
				"xsec_token": args.XsecToken,
				"tab":        args.Tab,
			}
			return convertToMCPResult(appServer.handleUserProfile(ctx, argsMap)), nil, nil
		}),
	)

	logrus.Info("registered 6 read-only MCP tools")
}

func convertToMCPResult(result *MCPToolResult) *mcp.CallToolResult {
	var contents []mcp.Content
	for _, c := range result.Content {
		switch c.Type {
		case "text":
			contents = append(contents, &mcp.TextContent{Text: c.Text})
		case "image":
			imageData, err := base64.StdEncoding.DecodeString(c.Data)
			if err != nil {
				logrus.WithError(err).Error("failed to decode base64 image data")
				contents = append(contents, &mcp.TextContent{Text: "图片数据解码失败: " + err.Error()})
			} else {
				contents = append(contents, &mcp.ImageContent{Data: imageData, MIMEType: c.MimeType})
			}
		}
	}

	return &mcp.CallToolResult{Content: contents, IsError: result.IsError}
}
