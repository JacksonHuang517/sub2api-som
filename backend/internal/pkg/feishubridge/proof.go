package feishubridge

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

type Claims struct {
	UserID   int64  `json:"uid"`
	Role     string `json:"role"`
	Session  string `json:"session"`
	Audience string `json:"aud"`
	Issued   int64  `json:"iat"`
	Expires  int64  `json:"exp"`
}

func Sign(c Claims, key string) string {
	b, _ := json.Marshal(c)
	p := base64.RawURLEncoding.EncodeToString(b)
	h := hmac.New(sha256.New, []byte(key))
	h.Write([]byte(p))
	return p + "." + base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}
func Verify(s, key string, now time.Time) (Claims, error) {
	var c Claims
	bad := errors.New("invalid proof")
	if len(key) < 32 || len(s) > 4096 {
		return c, bad
	}
	p := strings.Split(s, ".")
	if len(p) != 2 {
		return c, bad
	}
	sig, e := base64.RawURLEncoding.DecodeString(p[1])
	if e != nil {
		return c, bad
	}
	h := hmac.New(sha256.New, []byte(key))
	h.Write([]byte(p[0]))
	if !hmac.Equal(sig, h.Sum(nil)) {
		return c, bad
	}
	b, e := base64.RawURLEncoding.DecodeString(p[0])
	if e != nil || json.Unmarshal(b, &c) != nil {
		return c, bad
	}
	if c.UserID <= 0 || c.Session == "" || c.Audience != "som-feishu" || c.Expires <= now.Unix() || c.Issued > now.Unix()+5 || c.Expires-c.Issued > 60 || c.Expires <= c.Issued {
		return Claims{}, bad
	}
	return c, nil
}
