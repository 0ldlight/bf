-- BIN-142 文件引用的 nodes 投影（v2，仅供可信全新引导/测试显式应用；
-- New/Create/Update/Lookup 绝不执行 DDL）。v1 基础上新增七个可空、无
-- DEFAULT 的逐节点元数据列（created/modified/updated BIGINT；created_by/
-- modified_by VARCHAR(64)；sha1_original/md5_original VARCHAR(1024)）与
-- nodes_metadata_shape 形状约束：七列全 NULL（既有 v1 行的显式本地“未知
-- 历史”迁移策略——区别于目标全新 DDL 的 created/modified NOT NULL）或
-- created 与 modified 都非 NULL（updated 可 NULL）。既有 v1 库必须显式执行
-- metadata_upgrade.sql；CREATE TABLE IF NOT EXISTS 不视作升级。不是目标
-- 迁移/导入模式：无自动 ID/父目录、删除/GC、属性/统计列。
-- Nodes projection for file references (v2, applied explicitly by trusted
-- fresh bootstrap/tests only; New/Create/Update/Lookup never execute DDL).
-- On top of v1 it adds seven nullable, no-DEFAULT per-node metadata columns
-- (created/modified/updated BIGINT; created_by/modified_by VARCHAR(64);
-- sha1_original/md5_original VARCHAR(1024)) plus the nodes_metadata_shape
-- constraint: either all seven columns are NULL (the explicit local
-- unknown-history policy for existing v1 rows — distinct from the target
-- fresh DDL's NOT NULL created/modified) or created and modified are both
-- non-NULL (updated may stay NULL). An existing v1 database must apply
-- metadata_upgrade.sql explicitly; CREATE TABLE IF NOT EXISTS is not an
-- upgrade. Not a target migration/import schema: no auto IDs/parents,
-- deletion/GC, properties/stats.
CREATE TABLE IF NOT EXISTS nodes (
    node_id       bigint        NOT NULL,
    node_type     smallint      NOT NULL CHECK (node_type IN (0, 1)),
    repo          varchar(64)   NOT NULL COLLATE "C",
    node_path     varchar(1024) NOT NULL COLLATE "C",
    node_name     varchar(255)  NOT NULL COLLATE "C",
    depth         smallint      NOT NULL,
    bin_length    bigint,
    sha1_actual   varchar(40),
    md5_actual    varchar(32),
    sha256        varchar(64),
    created       bigint,
    modified      bigint,
    updated       bigint,
    created_by    varchar(64),
    modified_by   varchar(64),
    sha1_original varchar(1024),
    md5_original  varchar(1024),
    CONSTRAINT nodes_pk PRIMARY KEY (node_id),
    CONSTRAINT nodes_binaries_fk FOREIGN KEY (sha1_actual) REFERENCES binaries (sha1),
    CONSTRAINT nodes_metadata_shape CHECK (
        (created IS NULL AND modified IS NULL AND updated IS NULL
            AND created_by IS NULL AND modified_by IS NULL
            AND sha1_original IS NULL AND md5_original IS NULL)
        OR (created IS NOT NULL AND modified IS NOT NULL)
    )
);
CREATE UNIQUE INDEX IF NOT EXISTS nodes_repo_path_name_idx
    ON nodes (repo varchar_pattern_ops, node_path varchar_pattern_ops,
              node_name varchar_pattern_ops);
CREATE INDEX IF NOT EXISTS nodes_sha1_actual_idx ON nodes (sha1_actual);
