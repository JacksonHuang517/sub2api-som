package handler

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type feishuQuotaUserRepo struct{ service.UserRepository }

func (*feishuQuotaUserRepo) GetByID(context.Context, int64) (*service.User, error) {
	return &service.User{ID: 42, Status: service.StatusActive}, nil
}
func (*feishuQuotaUserRepo) GetUserAvatar(context.Context, int64) (*service.UserAvatar, error) {
	return nil, nil
}

type feishuQuotaSubRepo struct {
	service.UserSubscriptionRepository
}

func (*feishuQuotaSubRepo) ListByUserID(context.Context, int64) ([]service.UserSubscription, error) {
	return []service.UserSubscription{}, nil
}

func TestFeishuSubscriptionsCredentialAllowlistAndResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	key := "test-subscription-read-key-longer-than-32"
	t.Setenv("FEISHU_ALLOWED_USER_IDS", "42")
	svc := service.NewSubscriptionService(nil, &feishuQuotaSubRepo{}, nil, nil, nil)
	defer svc.Stop()
	h := NewSubscriptionHandler(svc)
	auth := &AuthHandler{userService: service.NewUserService(&feishuQuotaUserRepo{}, nil, nil, nil)}
	for _, tc := range []struct {
		name, key, token, id string
		status               int
	}{
		{"disabled", "", "", "42", 404},
		{"unauthorized", key, "", "42", 401},
		{"wrong key", key, "Bearer incorrect", "42", 401},
		{"invalid id", key, "Bearer " + key, "0", 404},
		{"not allowed", key, "Bearer " + key, "99", 403},
		{"success", key, "Bearer " + key, "42", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("FEISHU_BALANCE_READ_KEY", tc.key)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest("GET", "/", nil)
			c.Request.Header.Set("Authorization", tc.token)
			c.Params = gin.Params{{Key: "id", Value: tc.id}}
			h.FeishuSubscriptions(auth)(c)
			c.Writer.WriteHeaderNow()
			require.Equal(t, tc.status, w.Code)
			if tc.status == 200 {
				require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
				var body struct {
					Data service.UserQuotaSnapshot `json:"data"`
				}
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
				require.Equal(t, int64(42), body.Data.UserID)
				require.True(t, body.Data.Active)
				require.Equal(t, "USD", body.Data.Currency)
				require.NotNil(t, body.Data.Subscriptions)
			}
		})
	}
}
