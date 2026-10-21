-- BIN-135 显式本地号段预留原语（仅供可信全新引导/测试显式应用；
-- NewReservations/Current/Reserve 绝不执行 DDL）。unique_ids 的非负 CHECK
-- 是显式本地政策，不是目标迁移/导入模式；general 种子行对齐打包全新模式的
-- 同名引导事实，固定 general 命名空间，其他命名空间行不受影响。无 ON
-- CONFLICT/自动建行/并发引导；重复应用时种子 INSERT 不做 upsert、按原样
-- 报错。
-- Explicit local ID-range reservation primitive (applied explicitly by
-- trusted fresh bootstrap/tests only; NewReservations/Current/Reserve never
-- execute DDL). The nonnegative CHECK on unique_ids is explicit local policy,
-- not a target migration/import schema; the general seed row aligns with the
-- same-named bootstrap fact of the packaged fresh schema, fixes the general
-- namespace and leaves other namespace rows untouched. No ON CONFLICT,
-- automatic row creation or concurrent bootstrap; re-applying the seed INSERT
-- does no upsert and fails as-is.
CREATE TABLE IF NOT EXISTS unique_ids (
    index_type varchar(32) NOT NULL,
    current_id bigint       NOT NULL CHECK (current_id >= 0),
    CONSTRAINT unique_ids_pk PRIMARY KEY (index_type)
);
INSERT INTO unique_ids (index_type, current_id) VALUES ('general', 0);
