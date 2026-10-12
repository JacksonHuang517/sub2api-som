package store

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestLegacyDatabaseAddsSubscriptionTablesWithoutChangingWallet(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, e := sql.Open("sqlite", path)
	if e != nil {
		t.Fatal(e)
	}
	legacy := strings.Split(schema, "CREATE TABLE IF NOT EXISTS subscription_preferences")[0] + "PRAGMA user_version=1;"
	if _, e = db.Exec(legacy); e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec(`INSERT INTO bindings VALUES(7,'tenant','app','open','persisted',4,1,1); INSERT INTO preferences VALUES(7,1,5000000,1000000,9); INSERT INTO notification_outbox(event_id,user_id,binding_version,rule_version,cycle,level,balance_micros,threshold_micros,sampled_at,next_at,created_at) VALUES('wallet',7,4,9,0,1,1,5000000,1,1,1)`); e != nil {
		t.Fatal(e)
	}
	db.Close()
	db, e = Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	var version int64
	var enabled bool
	if e = db.QueryRow(`SELECT enabled,version FROM preferences WHERE user_id=7`).Scan(&enabled, &version); e != nil || !enabled || version != 9 {
		t.Fatal("wallet mutated", e)
	}
	if e = db.QueryRow(`SELECT version FROM bindings WHERE user_id=7`).Scan(&version); e != nil || version != 4 {
		t.Fatal("binding mutated", e)
	}
	var n int
	if e = db.QueryRow(`SELECT COUNT(*) FROM notification_outbox WHERE status='pending'`).Scan(&n); e != nil || n != 1 {
		t.Fatal("wallet queue mutated", e)
	}
	if e = db.QueryRow(`SELECT COUNT(*) FROM subscription_preferences`).Scan(&n); e != nil || n != 0 {
		t.Fatal("monitor enabled on migration", e)
	}
}
