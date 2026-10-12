package handler

import (
	"crypto/subtle"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/gin-gonic/gin"
)

// FeishuSubscriptions uses existing service dependencies without expanding the
// AuthHandler constructor or exposing a user's login credentials to the sidecar.
func (h *SubscriptionHandler) FeishuSubscriptions(auth *AuthHandler) gin.HandlerFunc {
	return func(c *gin.Context) {
		key := os.Getenv("FEISHU_BALANCE_READ_KEY")
		if len(key) < 32 {
			c.Status(http.StatusNotFound)
			return
		}
		header := c.GetHeader("Authorization")
		if !strings.HasPrefix(header, "Bearer ") || subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(header, "Bearer ")), []byte(key)) != 1 {
			c.Status(http.StatusUnauthorized)
			return
		}
		id, err := strconv.ParseInt(c.Param("id"), 10, 64)
		if err != nil || id <= 0 {
			c.Status(http.StatusNotFound)
			return
		}
		if !feishuUserAllowed(id) {
			c.Status(http.StatusForbidden)
			return
		}
		user, err := auth.userService.GetByID(c.Request.Context(), id)
		if err != nil || user == nil {
			c.Status(http.StatusServiceUnavailable)
			return
		}
		snapshot, err := h.subscriptionService.GetUserQuotaSnapshot(c.Request.Context(), id)
		if err != nil {
			c.Status(http.StatusServiceUnavailable)
			return
		}
		snapshot.Active = user.IsActive()
		c.Header("Cache-Control", "no-store")
		response.Success(c, snapshot)
	}
}
