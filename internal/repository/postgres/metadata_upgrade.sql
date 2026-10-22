-- BIN-142 既有 v1 nodes 表的一次性加列升级（单条 ALTER TABLE：七个可空、
-- 无 DEFAULT 的元数据列 + nodes_metadata_shape 形状约束）。仅供可信运维/
-- 测试在自有事务中显式执行；Store/启动从不执行任何 DDL，本语句也不是版本
-- 表/迁移引擎的一部分。重复执行以 42701（duplicate column）失败，绝不静默
-- 吞掉模式漂移；schema.sql 的 CREATE TABLE IF NOT EXISTS 不视作升级。
-- 对既有 v1 行，七列新增即全 NULL：这是显式本地 v1 历史迁移策略（保留
-- “从未记录元数据”的未知状态，不回填虚构时间），区别于目标全新 DDL 的
-- created/modified NOT NULL。
-- One-time additive upgrade for an existing v1 nodes table (a single ALTER
-- TABLE: seven nullable, no-DEFAULT metadata columns plus the
-- nodes_metadata_shape constraint). Trusted operators/tests execute it
-- explicitly inside their own transaction; the Store/startup never executes
-- DDL and this statement is not part of any version table or migration
-- engine. A repeat fails with 42701 (duplicate column) instead of silently
-- swallowing schema drift; schema.sql's CREATE TABLE IF NOT EXISTS is not
-- an upgrade. Existing v1 rows gain seven NULL columns: that is the
-- explicit local v1-history migration policy (preserving the never-recorded
-- unknown state without invented timestamps), distinct from the target
-- fresh DDL's NOT NULL created/modified.
ALTER TABLE nodes
    ADD COLUMN created bigint,
    ADD COLUMN modified bigint,
    ADD COLUMN updated bigint,
    ADD COLUMN created_by varchar(64),
    ADD COLUMN modified_by varchar(64),
    ADD COLUMN sha1_original varchar(1024),
    ADD COLUMN md5_original varchar(1024),
    ADD CONSTRAINT nodes_metadata_shape CHECK (
        (created IS NULL AND modified IS NULL AND updated IS NULL
            AND created_by IS NULL AND modified_by IS NULL
            AND sha1_original IS NULL AND md5_original IS NULL)
        OR (created IS NOT NULL AND modified IS NOT NULL)
    );