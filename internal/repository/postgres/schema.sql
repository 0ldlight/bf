-- BIN-132 文件引用的最小 nodes 投影（v1，仅供可信全新引导/测试显式应用；
-- New/Create/Update/Lookup 绝不执行 DDL）。不是目标迁移/导入模式：无自动
-- ID/父目录、删除/GC、时间戳/操作者/客户端校验和/属性/统计列。
-- Minimal nodes projection for file references (v1, applied explicitly by
-- trusted fresh bootstrap/tests only; New/Create/Update/Lookup never execute
-- DDL). Not a target migration/import schema: no auto IDs/parents,
-- deletion/GC, timestamps/actors/client checksums/properties/stats.
CREATE TABLE IF NOT EXISTS nodes (
    node_id     bigint        NOT NULL,
    node_type   smallint      NOT NULL CHECK (node_type IN (0, 1)),
    repo        varchar(64),
    node_path   varchar(1024),
    node_name   varchar(255)  NOT NULL COLLATE "C",
    depth       smallint      NOT NULL,
    bin_length  bigint,
    sha1_actual varchar(40),
    md5_actual  varchar(32),
    sha256      varchar(64),
    CONSTRAINT nodes_pk PRIMARY KEY (node_id),
    CONSTRAINT nodes_binaries_fk FOREIGN KEY (sha1_actual) REFERENCES binaries (sha1)
);
CREATE UNIQUE INDEX IF NOT EXISTS nodes_repo_path_name_idx
    ON nodes (repo varchar_pattern_ops, node_path varchar_pattern_ops,
              node_name varchar_pattern_ops);
CREATE INDEX IF NOT EXISTS nodes_sha1_actual_idx ON nodes (sha1_actual);
