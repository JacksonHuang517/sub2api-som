package handler

import (
	"encoding/json"
	"github.com/Wei-Shaw/sub2api/internal/pkg/feishubridge"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"net/http/httptest"
	"testing"
	"time"
)

func TestFeishuProofDisabledAndRequiresSubject(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &AuthHandler{}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/", nil)
	t.Setenv("FEISHU_PROOF_KEY", "")
	h.FeishuSessionProof(c)
	c.Writer.WriteHeaderNow()
	if w.Code != 404 {
		t.Fatal(w.Code)
	}
	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/", nil)
	t.Setenv("FEISHU_PROOF_KEY", "only-test-key-more-than-32-bytes-long")
	h.FeishuSessionProof(c)
	c.Writer.WriteHeaderNow()
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
}
func TestFeishuProofMinimalClaims(t *testing.T) {
	key := "only-test-key-more-than-32-bytes-long"
	t.Setenv("FEISHU_PROOF_KEY", key)
	t.Setenv("FEISHU_ALLOWED_USER_IDS", "42")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/", nil)
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 42})
	c.Set(string(middleware.ContextKeyUserRole), "user")
	c.Set(middleware.ContextKeySessionID, "original-session")
	(&AuthHandler{}).FeishuSessionProof(c)
	var v struct {
		Data struct {
			Proof string `json:"proof"`
		} `json:"data"`
	}
	if json.Unmarshal(w.Body.Bytes(), &v) != nil {
		t.Fatal("bad response")
	}
	claim, e := feishubridge.Verify(v.Data.Proof, key, time.Now())
	if e != nil || claim.UserID != 42 || claim.Role != "user" || claim.Session == "original-session" {
		t.Fatal("invalid claims", e)
	}
}
func TestFeishuReadCredentialAndAllowlist(t *testing.T) {
	key := "only-test-read-key-more-than-32-bytes"
	t.Setenv("FEISHU_BALANCE_READ_KEY", key)
	t.Setenv("FEISHU_ALLOWED_USER_IDS", "42")
	for _, token := range []string{"", "Bearer " + key} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("GET", "/", nil)
		c.Request.Header.Set("Authorization", token)
		c.Params = gin.Params{{Key: "id", Value: "99"}}
		(&AuthHandler{}).FeishuBalance(c)
		c.Writer.WriteHeaderNow()
		want := 401
		if token != "" {
			want = 403
		}
		if w.Code != want {
			t.Fatal(w.Code)
		}
	}
}

func TestFeishuProofRolloutAllowlist(t *testing.T) {
	t.Setenv("FEISHU_PROOF_KEY", "only-test-key-more-than-32-bytes-long")
	for _, tc := range []struct {
		name, list, role string
		id               int64
		status           int
	}{
		{"empty", "", "user", 42, 403},
		{"other", "99", "user", 42, 403},
		{"admin cannot bypass", "99", "admin", 42, 403},
		{"allowed", " 99, 42 ", "user", 42, 200},
		{"invalid identity", "0", "user", 0, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("FEISHU_ALLOWED_USER_IDS", tc.list)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest("POST", "/", nil)
			c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: tc.id})
			c.Set(string(middleware.ContextKeyUserRole), tc.role)
			c.Set(middleware.ContextKeySessionID, "test-session")
			(&AuthHandler{}).FeishuSessionProof(c)
			c.Writer.WriteHeaderNow()
			if w.Code != tc.status {
				t.Fatalf("status %d, want %d", w.Code, tc.status)
			}
			if tc.status != 200 && w.Body.Len() != 0 {
				t.Fatal("proof leaked for excluded user")
			}
		})
	}
}
