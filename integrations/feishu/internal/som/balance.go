package som

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"som/feishu/internal/domain"
	"time"
)

type Client struct {
	BaseURL, ReadKey string
	HTTP             *http.Client
}

func New(base, key string) (*Client, error) {
	u, e := url.Parse(base)
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost"))) {
		return nil, errors.New("invalid SOM origin")
	}
	if len(key) < 32 {
		return nil, errors.New("read key too short")
	}
	return &Client{base, key, &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *Client) Fetch(ctx context.Context, id int64) (domain.Snapshot, error) {
	var result domain.Snapshot
	req, e := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("%s/api/v1/integrations/feishu/balance/%d", c.BaseURL, id), nil)
	if e != nil {
		return result, errors.New("balance request failed")
	}
	req.Header.Set("Authorization", "Bearer "+c.ReadKey)
	resp, e := c.HTTP.Do(req)
	if e != nil {
		return result, errors.New("balance unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return result, errors.New("balance unavailable")
	}
	var body struct {
		Code *int `json:"code"`
		Data *struct {
			UserID   *int64    `json:"user_id"`
			Balance  *int64    `json:"balance_micros"`
			Currency string    `json:"currency"`
			Active   *bool     `json:"active"`
			At       time.Time `json:"at"`
		} `json:"data"`
	}
	e = json.NewDecoder(io.LimitReader(resp.Body, 32768)).Decode(&body)
	if e != nil || body.Code == nil || *body.Code != 0 || body.Data == nil {
		return result, errors.New("invalid balance response")
	}
	d := body.Data
	if d.UserID == nil || *d.UserID != id || d.Balance == nil || d.Active == nil || d.Currency != "USD" || d.At.IsZero() {
		return result, errors.New("invalid balance response")
	}
	return domain.Snapshot{UserID: *d.UserID, Balance: *d.Balance, Currency: d.Currency, Active: *d.Active, At: d.At}, nil
}
