package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// InvalidateOtherNamespaces runs before workers start. A different Feishu app's
// open_id must never be delivered using the new application's credentials.
// Empty tenant disables the optional enterprise allowlist, not identity namespacing.
func InvalidateOtherNamespaces(ctx context.Context, db *sql.DB, app, tenant string) error {
	if app == "" {
		return errors.New("namespace required")
	}
	tx, e := db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	_, e = tx.ExecContext(ctx, "INSERT INTO audit_events(user_id,action,at) SELECT user_id,'namespace_changed',? FROM bindings WHERE active=1 AND (app_id<>? OR (?<>'' AND tenant_key<>?))", time.Now().Unix(), app, tenant, tenant)
	if e != nil {
		return e
	}
	for _, q := range []string{
		"UPDATE preferences SET enabled=0,version=version+1 WHERE user_id IN (SELECT user_id FROM bindings WHERE active=1 AND (app_id<>? OR (?<>'' AND tenant_key<>?)))",
		"UPDATE notification_outbox SET status='cancelled',last_error='APP_CHANGED' WHERE status='pending' AND user_id IN (SELECT user_id FROM bindings WHERE active=1 AND (app_id<>? OR (?<>'' AND tenant_key<>?)))",
		"UPDATE notification_outbox SET status='uncertain',last_error='APP_CHANGED_DURING_LEASE' WHERE status='leased' AND user_id IN (SELECT user_id FROM bindings WHERE active=1 AND (app_id<>? OR (?<>'' AND tenant_key<>?)))",
		"UPDATE subscription_preferences SET enabled=0,version=version+1 WHERE user_id IN (SELECT user_id FROM bindings WHERE active=1 AND (app_id<>? OR (?<>'' AND tenant_key<>?)))",
		"UPDATE subscription_outbox SET status='cancelled',last_error='APP_CHANGED' WHERE status='pending' AND user_id IN (SELECT user_id FROM bindings WHERE active=1 AND (app_id<>? OR (?<>'' AND tenant_key<>?)))",
		"UPDATE subscription_outbox SET status='uncertain',last_error='APP_CHANGED_DURING_LEASE' WHERE status='leased' AND user_id IN (SELECT user_id FROM bindings WHERE active=1 AND (app_id<>? OR (?<>'' AND tenant_key<>?)))",
		"UPDATE bindings SET active=0,version=version+1,open_id='',display_name='' WHERE active=1 AND (app_id<>? OR (?<>'' AND tenant_key<>?))",
		"DELETE FROM oauth_attempts WHERE consumed=2 AND (app_id<>? OR (?<>'' AND tenant_key<>?))",
	} {
		if _, e = tx.ExecContext(ctx, q, app, tenant, tenant); e != nil {
			return e
		}
	}
	return tx.Commit()
}
