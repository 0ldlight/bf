// 身份列约束回归：repo/node_path/node_name 三列都必须 NOT NULL 且显式
// COLLATE "C"（对齐目标全新模式的 NOT NULL 键与本仓确定性排序策略）。
// Identity-column regression: repo/node_path/node_name must all be NOT
// NULL with explicit COLLATE "C" (matching the target fresh-schema NOT
// NULL keys and this repo's deterministic local collation policy).
package postgresit

import (
	"testing"
)

func TestNodeSchemaIdentityConstraints(t *testing.T) {
	_, schema := nodeFixture(t)
	adm := adminPool(t)
	ctx := nodeTestContext(t)
	rows, err := adm.QueryContext(ctx, `
		SELECT a.attname, a.attnotnull, coalesce(c.collname, '')
		FROM pg_attribute a
		JOIN pg_class r ON r.oid = a.attrelid
		JOIN pg_namespace n ON n.oid = r.relnamespace
		LEFT JOIN pg_collation c ON c.oid = a.attcollation
		WHERE n.nspname = $1 AND r.relname = 'nodes'
		  AND a.attname IN ('repo', 'node_path', 'node_name')
		  AND a.attnum > 0 AND NOT a.attisdropped`, schema)
	if err != nil {
		t.Fatalf("query catalog: %v", err)
	}
	defer rows.Close()
	notNull := map[string]bool{}
	collation := map[string]string{}
	for rows.Next() {
		var name, coll string
		var nn bool
		if err := rows.Scan(&name, &nn, &coll); err != nil {
			t.Fatalf("scan catalog row: %v", err)
		}
		notNull[name] = nn
		collation[name] = coll
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("catalog iteration: %v", err)
	}
	if len(notNull) != 3 {
		t.Fatalf("catalog returned %d identity columns (%v), want 3", len(notNull), notNull)
	}
	for _, col := range []string{"repo", "node_path", "node_name"} {
		if !notNull[col] {
			t.Errorf("nodes.%s must be NOT NULL", col)
		}
		if collation[col] != "C" {
			t.Errorf("nodes.%s collation = %q, want explicit \"C\"", col, collation[col])
		}
	}
}
