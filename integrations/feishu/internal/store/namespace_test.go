package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestChangedAppInvalidatesIdentityAndOutbox(t *testing.T) {
	db, e := Open(filepath.Join(t.TempDir(), "db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	_, e = db.Exec("INSERT INTO bindings VALUES(1,'tenant','old-app','ou_old','Demo',1,1,1); INSERT INTO preferences(user_id,enabled) VALUES(1,1); INSERT INTO notification_outbox(event_id,user_id,binding_version,rule_version,cycle,level,balance_micros,threshold_micros,sampled_at,next_at,created_at) VALUES('e',1,1,1,1,1,1,5,1,1,1)")
	if e != nil {
		t.Fatal(e)
	}
	if e = InvalidateOtherNamespaces(context.Background(), db, "new-app", "tenant"); e != nil {
		t.Fatal(e)
	}
	var active, enabled int
	var status, openid string
	db.QueryRow("SELECT active,open_id FROM bindings").Scan(&active, &openid)
	db.QueryRow("SELECT enabled FROM preferences").Scan(&enabled)
	db.QueryRow("SELECT status FROM notification_outbox").Scan(&status)
	if active != 0 || enabled != 0 || status != "cancelled" || openid != "" {
		t.Fatal(active, enabled, status, openid)
	}
}

func TestOptionalTenantAllowlist(t *testing.T) {
	db, e := Open(filepath.Join(t.TempDir(), "db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	_, e = db.Exec("INSERT INTO bindings VALUES(1,'tenant-a','current','ou_same','A',1,1,1),(2,'tenant-b','current','ou_same','B',1,1,1),(3,'tenant-a','old-app','ou_old','C',1,1,1)")
	if e != nil {
		t.Fatal(e)
	}
	if e = InvalidateOtherNamespaces(context.Background(), db, "current", ""); e != nil {
		t.Fatal(e)
	}
	var count int
	if e = db.QueryRow("SELECT COUNT(*) FROM bindings WHERE active=1").Scan(&count); e != nil || count != 2 {
		t.Fatal("app isolation or tenant preservation failed", count, e)
	}
	if e = InvalidateOtherNamespaces(context.Background(), db, "current", "tenant-a"); e != nil {
		t.Fatal(e)
	}
	if e = db.QueryRow("SELECT COUNT(*) FROM bindings WHERE active=1").Scan(&count); e != nil || count != 1 {
		t.Fatal("explicit allowlist ignored", count, e)
	}
}

func TestNamespaceChangeDisablesAndCancelsSubscriptions(t *testing.T) {
	db, e := Open(filepath.Join(t.TempDir(), "db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	_, e = db.Exec(`INSERT INTO bindings VALUES(1,'tenant','old-app','ou_old','Demo',1,1,1); INSERT INTO subscription_preferences(user_id,enabled) VALUES(1,1); INSERT INTO subscription_outbox(event_id,user_id,subscription_id,kind,window_start,limit_micros,binding_version,rule_version,cycle,level,sampled_at,next_at,created_at) VALUES('pending',1,10,'daily','window',100,1,1,1,1,1,1,1),('leased',1,11,'weekly','window',100,1,1,1,1,1,1,1); UPDATE subscription_outbox SET status='leased' WHERE event_id='leased'`)
	if e != nil {
		t.Fatal(e)
	}
	if e = InvalidateOtherNamespaces(context.Background(), db, "new-app", "tenant"); e != nil {
		t.Fatal(e)
	}
	var enabled bool
	var pending, leased string
	if e = db.QueryRow(`SELECT enabled FROM subscription_preferences`).Scan(&enabled); e != nil || enabled {
		t.Fatal("subscription still enabled", e)
	}
	db.QueryRow(`SELECT status FROM subscription_outbox WHERE event_id='pending'`).Scan(&pending)
	db.QueryRow(`SELECT status FROM subscription_outbox WHERE event_id='leased'`).Scan(&leased)
	if pending != "cancelled" || leased != "uncertain" {
		t.Fatal(pending, leased)
	}
}
