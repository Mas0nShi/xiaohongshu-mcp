package main

import (
	"crypto/subtle"
	"net/http"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

// corsMiddleware CORS 中间件
func corsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization, Accept, Mcp-Session-Id, MCP-Protocol-Version, Last-Event-ID")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}

		c.Next()
	}
}

// bearerAuthMiddleware 启用可选的 Bearer Token 鉴权。
//
// 默认不启用鉴权以保持向后兼容；设置 MCP_BEARER_TOKEN 后，
// /mcp 和 /api/v1 路由需要提供：
//
//	Authorization: Bearer <MCP_BEARER_TOKEN>
//
// 也兼容 XHS_MCP_BEARER_TOKEN 作为同义环境变量。
func bearerAuthMiddleware() gin.HandlerFunc {
	token := strings.TrimSpace(os.Getenv("MCP_BEARER_TOKEN"))
	if token == "" {
		token = strings.TrimSpace(os.Getenv("XHS_MCP_BEARER_TOKEN"))
	}

	if token == "" {
		return func(c *gin.Context) {
			c.Next()
		}
	}

	logrus.Info("Bearer token authentication enabled for /mcp and /api/v1 routes")

	return func(c *gin.Context) {
		if c.Request.Method == http.MethodOptions {
			c.Next()
			return
		}

		authHeader := c.GetHeader("Authorization")
		actualToken, ok := parseBearerToken(authHeader)
		if !ok || subtle.ConstantTimeCompare([]byte(actualToken), []byte(token)) != 1 {
			c.Header("WWW-Authenticate", `Bearer realm="xiaohongshu-mcp"`)
			c.AbortWithStatusJSON(http.StatusUnauthorized, ErrorResponse{
				Error: "unauthorized",
				Code:  "UNAUTHORIZED",
			})
			return
		}

		c.Next()
	}
}

func parseBearerToken(header string) (string, bool) {
	parts := strings.Fields(header)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return "", false
	}

	return parts[1], true
}

// errorHandlingMiddleware 错误处理中间件
func errorHandlingMiddleware() gin.HandlerFunc {
	return gin.CustomRecovery(func(c *gin.Context, recovered any) {
		logrus.Errorf("服务器内部错误: %v, path: %s", recovered, c.Request.URL.Path)

		respondError(c, http.StatusInternalServerError, "INTERNAL_ERROR",
			"服务器内部错误", recovered)
	})
}
