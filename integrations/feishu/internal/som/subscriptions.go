package som

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"som/feishu/internal/domain"
	"som/feishu/internal/subscriptions"
	"time"
)

func required(raw json.RawMessage, names ...string) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return false
	}
	for _, name := range names {
		v, ok := fields[name]
		if !ok || string(v) == "null" {
			return false
		}
	}
	return true
}
func (c *Client) FetchSubscriptions(ctx context.Context, id int64) (domain.SubscriptionSnapshot, error) {
	var result domain.SubscriptionSnapshot
	req, e := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("%s/api/v1/integrations/feishu/subscriptions/%d", c.BaseURL, id), nil)
	if e != nil {
		return result, errors.New("subscription request failed")
	}
	req.Header.Set("Authorization", "Bearer "+c.ReadKey)
	resp, e := c.HTTP.Do(req)
	if e != nil {
		return result, errors.New("subscription unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return result, errors.New("subscription unavailable")
	}
	var body struct {
		Code *int            `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body) != nil || body.Code == nil || *body.Code != 0 || !required(body.Data, "user_id", "active", "currency", "at", "subscriptions") {
		return result, errors.New("invalid subscription response")
	}
	var wire struct {
		Subscriptions []json.RawMessage `json:"subscriptions"`
	}
	if json.Unmarshal(body.Data, &wire) != nil {
		return result, errors.New("invalid subscription response")
	}
	for _, raw := range wire.Subscriptions {
		if !required(raw, "subscription_id", "group_id", "group_name", "status", "expires_at", "windows") {
			return result, errors.New("invalid subscription response")
		}
		var sub struct {
			Windows []json.RawMessage `json:"windows"`
		}
		if json.Unmarshal(raw, &sub) != nil {
			return result, errors.New("invalid subscription response")
		}
		for _, q := range sub.Windows {
			if !required(q, "kind", "limited", "used_micros") {
				return result, errors.New("invalid subscription response")
			}
		}
	}
	if json.Unmarshal(body.Data, &result) != nil {
		return result, errors.New("invalid subscription response")
	}
	if e = subscriptions.Validate(result, id, time.Now()); e != nil {
		return domain.SubscriptionSnapshot{}, e
	}
	return result, nil
}
