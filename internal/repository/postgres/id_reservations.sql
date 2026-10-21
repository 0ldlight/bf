-- BIN-135 显式本地号段预留原语（仅供可信全新引导/测试显式应用；
-- NewReservations/Current/Reserve 绝不执行 DDL）。来源限定：打包全新
-- postgresql.sql 只建 unique_ids 表、不含 general 种子 INSERT；目标
-- general 零值初始化来自 IdGenerator.initializeIdGenerator 的缺行写零
-- ——本种子行是显式本地全新引导投影，非复制目标 SQL；非负 CHECK 亦是
-- 显式本地政策。固定 general 命名空间，其他命名空间行不受影响；无 ON
-- CONFLICT/自动建行/并发引导。DDL 不带 IF NOT EXISTS（仅限全新应用）：
-- 重复应用在 CREATE TABLE 或种子 INSERT 处按原样报错，无 upsert。
-- Explicit local ID-range reservation primitive (applied explicitly by
-- trusted fresh bootstrap/tests only; NewReservations/Current/Reserve never
-- execute DDL). Provenance: the packaged fresh postgresql.sql only creates
-- the unique_ids table and carries no general seed INSERT; the target's
-- general zero initialization belongs to IdGenerator.initializeIdGenerator
-- writing zero for an absent row — this seed row is an explicit local
-- fresh-bootstrap projection, not copied target SQL, and the nonnegative
-- CHECK is likewise explicit local policy. The general namespace is fixed,
-- other namespace rows are untouched, and there is no ON CONFLICT, automatic
-- row creation or concurrent bootstrap. The DDL carries no IF NOT EXISTS
-- (fresh-only application): re-application fails as-is at the CREATE TABLE
-- or the seed INSERT, with no upsert.
CREATE TABLE unique_ids (
    index_type varchar(32) NOT NULL,
    current_id bigint       NOT NULL CHECK (current_id >= 0),
    CONSTRAINT unique_ids_pk PRIMARY KEY (index_type)
);
INSERT INTO unique_ids (index_type, current_id) VALUES ('general', 0);
