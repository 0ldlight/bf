-- BIN-129 首个全新数据库模式（v1）。这不是生产升级/迁移脚本，也没有节点表、
-- 外键或迁移框架；调用方在可信引导/测试中显式应用（embedded SchemaSQL），
-- New/Register 绝不自动执行任何 DDL。
-- BIN-129 first fresh-database schema (v1). Not a production upgrade or
-- migration guarantee, with no node table, foreign keys or migration
-- framework; callers apply it explicitly (embedded SchemaSQL) for trusted
-- bootstrap or tests — New/Register never execute DDL automatically.
--
-- sha256 保持可空：NULL 显式表示历史缺失，待回填；非空但 Java-blank 的
-- 字符串同样按缺失处理。bin_length 为非负约束。
-- sha256 stays nullable: NULL explicitly represents a historically missing
-- value awaiting backfill; a stored non-NULL Java-blank string is treated
-- the same way. bin_length carries a nonnegative constraint.
CREATE TABLE IF NOT EXISTS binaries (
    sha1       varchar(40)  PRIMARY KEY,
    md5        varchar(32)  NOT NULL,
    bin_length bigint       NOT NULL CHECK (bin_length >= 0),
    sha256     varchar(64)
);
