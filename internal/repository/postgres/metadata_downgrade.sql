-- BIN-142 v2 → v1 的物理回退（单一原子 DO 语句；首选回滚二进制、保留附加
-- 列）。守卫全部 fail-closed 且不改变任何全局/会话配置：
--   1. 仅限 READ COMMITTED——其他隔离级别以固定 SQLSTATE 25000 拒绝：旧
--      快照可能漏看取锁前刚提交的行；
--   2. 取 nodes 的 ACCESS EXCLUSIVE 表锁后再检查，保证后续行检查看到取锁
--      前已提交的全部行；
--   3. 启用行级安全（pg_class.relrowsecurity）的表以固定 55000 拒绝——
--      不依赖“策略恰好隐藏行”的偶然性；
--   4. 任何行的七个新列有任何非 NULL 值即以固定 55000 拒绝——绝不自动
--      清空数据来通过守卫；
--   5. 仅删除 nodes_metadata_shape 约束与七个元数据列。
-- 所有守卫与删除在同一 DO 语句内：任一守卫失败时整个语句回滚，无部分删
-- 列。表名沿用受信任 search_path 契约（与 schema.sql/升级语句一致，非模式
-- 限定）。不改变任何权限或 RLS 策略。
-- Physical v2 → v1 downgrade (one atomic DO statement; prefer rolling back
-- the binary while keeping the added columns). Every guard is fail-closed
-- and no global/session configuration changes:
--   1. READ COMMITTED only — any other isolation level is rejected with the
--      fixed SQLSTATE 25000: an older snapshot could miss rows committed
--      just before the lock;
--   2. the ACCESS EXCLUSIVE table lock on nodes is taken BEFORE the row
--      checks so they observe every row committed before the lock;
--   3. a row-security-enabled table (pg_class.relrowsecurity) is rejected
--      with the fixed 55000 — never relying on policies happening to hide
--      rows;
--   4. any nonnull value in any of the seven new columns on any row rejects
--      with the fixed 55000 — data is never auto-cleared to pass the guard;
--   5. only the nodes_metadata_shape constraint and the seven metadata
--      columns are dropped.
-- All guards and drops live in one DO statement: any guard failure rolls
-- the whole statement back with no partial column removal. The table name
-- follows the trusted search_path contract (unqualified, like schema.sql
-- and the upgrade statement). No privileges or RLS policies are touched.
DO $binflow_nodes_metadata_down$
DECLARE
    nodes_oid oid;
BEGIN
    IF current_setting('transaction_isolation') <> 'read committed' THEN
        RAISE EXCEPTION USING ERRCODE = '25000',
            MESSAGE = 'nodes metadata downgrade requires READ COMMITTED isolation';
    END IF;
    LOCK TABLE nodes IN ACCESS EXCLUSIVE MODE;
    nodes_oid := 'nodes'::regclass;
    IF (SELECT relrowsecurity FROM pg_class WHERE oid = nodes_oid) THEN
        RAISE EXCEPTION USING ERRCODE = '55000',
            MESSAGE = 'nodes metadata downgrade refuses a row-security-enabled nodes table';
    END IF;
    IF EXISTS (
        SELECT 1 FROM nodes
        WHERE created IS NOT NULL OR modified IS NOT NULL OR updated IS NOT NULL
           OR created_by IS NOT NULL OR modified_by IS NOT NULL
           OR sha1_original IS NOT NULL OR md5_original IS NOT NULL
    ) THEN
        RAISE EXCEPTION USING ERRCODE = '55000',
            MESSAGE = 'nodes metadata downgrade refuses to drop populated metadata columns';
    END IF;
    ALTER TABLE nodes DROP CONSTRAINT nodes_metadata_shape;
    ALTER TABLE nodes
        DROP COLUMN created,
        DROP COLUMN modified,
        DROP COLUMN updated,
        DROP COLUMN created_by,
        DROP COLUMN modified_by,
        DROP COLUMN sha1_original,
        DROP COLUMN md5_original;
END
$binflow_nodes_metadata_down$;