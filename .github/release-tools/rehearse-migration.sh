#!/usr/bin/env bash
# Restore schema only and seed synthetic data; never load production row data.
set -euo pipefail
helper=backend/cmd/ci-migration-rehearsal
test ! -e "$helper"
mkdir "$helper"
trap 'rm -f "$helper/main.go"; rmdir "$helper"; rm -f "$RUNNER_TEMP/rehearsal-schema.sql" "$RUNNER_TEMP/rehearsal-migrations.csv"' EXIT
python3 - <<'PY'
import base64, gzip, os, pathlib
p = pathlib.Path(os.environ['RUNNER_TEMP'])
schema = os.environ['SCHEMA_PART_1'] + os.environ['SCHEMA_PART_2']
assert schema and os.environ['MIGRATIONS_B64'], 'Missing private schema inputs'
(p / 'rehearsal-schema.sql').write_bytes(gzip.decompress(base64.b64decode(schema)))
(p / 'rehearsal-migrations.csv').write_bytes(gzip.decompress(base64.b64decode(os.environ['MIGRATIONS_B64'])))
PY
docker exec -i "$PG_CONTAINER" psql -U postgres -d migration_rehearsal -v ON_ERROR_STOP=1 < "$RUNNER_TEMP/rehearsal-schema.sql" > /dev/null
docker exec -i "$PG_CONTAINER" psql -U postgres -d migration_rehearsal -v ON_ERROR_STOP=1 -c "COPY schema_migrations(filename,checksum) FROM STDIN WITH CSV HEADER" < "$RUNNER_TEMP/rehearsal-migrations.csv"
docker exec -i "$PG_CONTAINER" psql -U postgres -d migration_rehearsal -v ON_ERROR_STOP=1 <<'SQL'
INSERT INTO users(id,email,password_hash,balance,frozen_balance,total_recharged) VALUES(900001,'migration@example.invalid','not-a-password',123.45678901,2.25,200);
INSERT INTO groups(id,name,platform) VALUES(900001,'Migration OpenAI','openai'),(900002,'Migration retired','antigravity');
INSERT INTO accounts(id,name,platform,type) VALUES(900001,'Synthetic OpenAI','openai','oauth'),(900002,'Synthetic retired','antigravity','oauth');
INSERT INTO account_groups(account_id,group_id) VALUES(900001,900001),(900002,900002);
INSERT INTO api_keys(id,user_id,key,name,group_id,quota,quota_used) VALUES(900001,900001,'synthetic-ci-key-not-valid','Migration key',900001,50,4.25);
INSERT INTO user_subscriptions(id,user_id,group_id,starts_at,expires_at,daily_usage_usd,weekly_usage_usd,monthly_usage_usd) VALUES(900001,900001,900001,'2026-01-01','2030-01-01',1.25,2.50,3.75);
INSERT INTO payment_orders(id,user_id,amount,pay_amount,expires_at,status,out_trade_no) VALUES(900001,900001,20,20,'2030-01-01','COMPLETED','synthetic-migration-order');
INSERT INTO usage_logs(id,user_id,api_key_id,account_id,group_id,model,total_cost,actual_cost,input_tokens,output_tokens) VALUES(900001,900001,900001,900001,900001,'synthetic-model',0.5,0.25,100,20);
INSERT INTO settings(key,value) VALUES('site_name','Synthetic Site'),('site_logo','/favicon.ico?v=existing-brand');
SQL
cat > "$helper/main.go" <<'GO'
package main

import (
    "context"
    "database/sql"
    "fmt"
    "os"
    "strings"
    "time"
    "github.com/Wei-Shaw/sub2api/internal/repository"
    _ "github.com/lib/pq"
)

func must(err error) { if err != nil { panic(err) } }

func main() {
    ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
    defer cancel()
    dsn := os.Getenv("REHEARSAL_DSN")
    if !strings.Contains(dsn, "@127.0.0.1:5432/migration_rehearsal?") { panic("local rehearsal database required") }
    db, err := sql.Open("postgres", dsn); must(err); defer db.Close()
    checks := map[string]string{
        "users": "SELECT md5(string_agg(concat_ws('|',id,email,password_hash,balance,frozen_balance,total_recharged),',' ORDER BY id)) FROM users",
        "api_keys": "SELECT md5(string_agg(concat_ws('|',id,user_id,key,group_id,quota,quota_used),',' ORDER BY id)) FROM api_keys",
        "subscriptions": "SELECT md5(string_agg(row_to_json(t)::text,',' ORDER BY id)) FROM user_subscriptions t",
        "payments": "SELECT md5(string_agg(concat_ws('|',id,user_id,amount,pay_amount,status,out_trade_no),',' ORDER BY id)) FROM payment_orders",
        "usage": "SELECT md5(string_agg(concat_ws('|',id,user_id,api_key_id,account_id,total_cost,actual_cost,input_tokens,output_tokens),',' ORDER BY id)) FROM usage_logs",
        "branding": "SELECT md5(string_agg(key || '=' || value,',' ORDER BY key)) FROM settings WHERE key IN ('site_name','site_logo')",
    }
    before := map[string]string{}
    for name, query := range checks { var v string; must(db.QueryRowContext(ctx,query).Scan(&v)); before[name]=v }
    var initial int; must(db.QueryRowContext(ctx,"SELECT count(*) FROM schema_migrations").Scan(&initial))
    started := time.Now()
    must(repository.ApplyMigrations(ctx,db))
    var first int; must(db.QueryRowContext(ctx,"SELECT count(*) FROM schema_migrations").Scan(&first))
    must(repository.ApplyMigrations(ctx,db))
    var second int; must(db.QueryRowContext(ctx,"SELECT count(*) FROM schema_migrations").Scan(&second))
    if first != second || first <= initial { panic("migration count/idempotence failed") }
    for name, query := range checks { var v string; must(db.QueryRowContext(ctx,query).Scan(&v)); if v != before[name] { panic(name+" changed") }; fmt.Println(name+" preserved") }
    var checksum string
    must(db.QueryRowContext(ctx,"SELECT checksum FROM schema_migrations WHERE filename='237_add_minimax_platform.sql'").Scan(&checksum))
    if checksum != "f4c73d2dbce114ca7ade1aac51998c3465490f4f3c9b3e868e53590f3fa8601b" { panic("historical checksum rewritten") }
    var invalid int
    must(db.QueryRowContext(ctx,"SELECT count(*) FROM pg_constraint WHERE conname IN ('user_platform_quotas_platform_check','composite_model_routes_target_platform_check')").Scan(&invalid))
    if invalid != 0 { panic("final platform constraints not reconciled") }
    var status string; var schedulable bool
    must(db.QueryRowContext(ctx,"SELECT status,schedulable FROM accounts WHERE id=900002").Scan(&status,&schedulable))
    if status != "disabled" || schedulable { panic("retired account remains scheduled") }
    fmt.Printf("PASS: %d new migrations, repeat run idempotent, historical checksum retained, synthetic business data preserved; duration=%s\n",first-initial,time.Since(started))
}
GO
(cd backend && go run ./cmd/ci-migration-rehearsal) | tee "$RUNNER_TEMP/migration-result.txt"
