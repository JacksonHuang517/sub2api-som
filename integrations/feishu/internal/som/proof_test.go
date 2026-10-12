package som

import (
	"testing"
	"time"
)

func TestProofSecurity(t *testing.T) {
	key := "test-only-proof-key-more-than-32-bytes"
	now := time.Now()
	valid := Claims{UserID: 1, Role: "user", Session: "a", Audience: "som-feishu", Issued: now.Unix(), Expires: now.Add(time.Minute).Unix()}
	if _, e := Verify(Sign(valid, key), key, now); e != nil {
		t.Fatal(e)
	}
	cases := []Claims{valid, valid, valid, valid}
	cases[0].Audience = "som-login"
	cases[1].Expires = now.Unix()
	cases[2].Expires = now.Add(2 * time.Minute).Unix()
	cases[3].Issued = now.Add(time.Minute).Unix()
	for _, c := range cases {
		if _, e := Verify(Sign(c, key), key, now); e == nil {
			t.Fatal("accepted invalid proof")
		}
	}
	if _, e := Verify(Sign(valid, key)+"x", key, now); e == nil {
		t.Fatal("signature tamper")
	}
}
