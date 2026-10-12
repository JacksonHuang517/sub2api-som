package handler

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/feishubridge"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
)

// FeishuSessionProof is reachable ONLY behind the existing JWT/session middleware.
// No SOM login tokens are exposed to the independent notification service.
func (h *AuthHandler) FeishuSessionProof(c *gin.Context) {
	key := os.Getenv("FEISHU_PROOF_KEY")
	if len(key) < 32 {
		c.Status(http.StatusNotFound)
		return
	}
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		c.Status(401)
		return
	}
	if !feishuUserAllowed(subject.UserID) {
		c.Status(http.StatusForbidden)
		return
	}
	role, ok := middleware.GetUserRoleFromContext(c)
	if !ok {
		c.Status(401)
		return
	}
	session := c.GetString(middleware.ContextKeySessionID)
	if session == "" {
		session = c.GetHeader("Authorization")
	}
	digest := sha256.Sum256([]byte(session))
	now := time.Now()
	claims := feishubridge.Claims{UserID: subject.UserID, Role: role, Session: hex.EncodeToString(digest[:]), Audience: "som-feishu", Issued: now.Unix(), Expires: now.Add(time.Minute).Unix()}
	c.Header("Cache-Control", "no-store")
	response.Success(c, gin.H{"proof": feishubridge.Sign(claims, key)})
}

// FeishuBalance is deliberately a tiny data plane: a separate revocable key and
// operator-controlled user allowlist. It has no database mutation or admin access.
func (h *AuthHandler) FeishuBalance(c *gin.Context) {
	key := os.Getenv("FEISHU_BALANCE_READ_KEY")
	if len(key) < 32 {
		c.Status(404)
		return
	}
	supplied := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
	if subtle.ConstantTimeCompare([]byte(supplied), []byte(key)) != 1 || !strings.HasPrefix(c.GetHeader("Authorization"), "Bearer ") {
		c.Status(401)
		return
	}
	id, e := strconv.ParseInt(c.Param("id"), 10, 64)
	if e != nil || id <= 0 {
		c.Status(404)
		return
	}
	if !feishuUserAllowed(id) {
		c.Status(403)
		return
	}
	u, e := h.userService.GetByID(c.Request.Context(), id)
	if e != nil {
		c.Status(503)
		return
	}
	if math.IsNaN(u.Balance) || math.IsInf(u.Balance, 0) || math.Abs(u.Balance) > 1e9 {
		c.Status(503)
		return
	}
	amount := decimal.NewFromFloat(u.Balance).Shift(6).Round(0).IntPart()
	c.Header("Cache-Control", "no-store")
	response.Success(c, gin.H{"user_id": id, "balance_micros": amount, "currency": "USD", "active": u.IsActive(), "at": time.Now().UTC()})
}

// The same explicit rollout allowlist gates identity binding and balance reads.
func feishuUserAllowed(id int64) bool {
	if id <= 0 {
		return false
	}
	for _, value := range strings.Split(os.Getenv("FEISHU_ALLOWED_USER_IDS"), ",") {
		if strings.TrimSpace(value) == strconv.FormatInt(id, 10) {
			return true
		}
	}
	return false
}
