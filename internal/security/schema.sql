-- BIN-201 可信下载安全基础 schema v1（BinFlow 本地命名，非 Access 数据库
-- 克隆）。仅供可信运维/测试在自有事务中显式应用；Store/启动从不执行任何
-- DDL，也不自动创建管理员、组或 read 授权——唯一自动插入的是版本行 1。
-- DDL 不带 IF NOT EXISTS（仅限全新应用）：重复应用按原样报错，绝不静默
-- 吞掉模式漂移。授权模型是仓库级 read（scope='**'）：更窄的 glob/exclude
-- 输入被显式拒绝而不是放宽为允许；支持组授权与直授权，无特权默认组。
-- BIN-201 trusted-download security foundation schema v1 (BinFlow-local
-- names, not an Access database clone). Trusted operators/tests apply it
-- explicitly inside their own transaction; the Store/startup never executes
-- DDL and creates no implicit admin, group or read grant — the only
-- automatic insert is the version row 1. No IF NOT EXISTS (fresh-only
-- application): re-application fails as-is instead of silently swallowing
-- schema drift. The grant model is repository-wide read (scope='**'):
-- narrower glob/exclude inputs are rejected explicitly rather than widened
-- to allow; both group grants and direct grants are supported with no
-- privileged default group.
CREATE TABLE security_schema_version (
    version integer PRIMARY KEY CHECK (version = 1)
);
INSERT INTO security_schema_version (version) VALUES (1);

CREATE TABLE security_users (
    user_id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    username        varchar(64) UNIQUE NOT NULL
                        CHECK (username ~ '^[a-z0-9][a-z0-9._@-]{0,63}$'),
    encoded_secret  text NOT NULL
                        CHECK (char_length(encoded_secret) BETWEEN 1 AND 256),
    enabled         boolean NOT NULL,
    effective_admin boolean NOT NULL
);

CREATE TABLE security_groups (
    group_id   bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    group_name varchar(64) UNIQUE NOT NULL
                   CHECK (group_name ~ '^[a-z0-9][a-z0-9._@-]{0,63}$')
);

CREATE TABLE security_group_members (
    user_id  bigint NOT NULL REFERENCES security_users(user_id) ON DELETE CASCADE,
    group_id bigint NOT NULL REFERENCES security_groups(group_id) ON DELETE CASCADE,
    PRIMARY KEY (user_id, group_id)
);

CREATE TABLE security_repositories (
    repo_key     varchar(64) PRIMARY KEY
                     CHECK (repo_key ~ '^[A-Za-z0-9._-]+$'
                            AND repo_key NOT IN ('.', '..')),
    kind        text   NOT NULL CHECK (kind = 'local'),
    package_type text  NOT NULL CHECK (package_type = 'generic'),
    revision    bigint NOT NULL CHECK (revision > 0)
);

CREATE TABLE security_read_grants (
    repo_key varchar(64) NOT NULL REFERENCES security_repositories(repo_key) ON DELETE CASCADE,
    user_id  bigint REFERENCES security_users(user_id) ON DELETE CASCADE,
    group_id bigint REFERENCES security_groups(group_id) ON DELETE CASCADE,
    scope    text NOT NULL CHECK (scope = '**'),
    CHECK ((user_id IS NOT NULL AND group_id IS NULL)
           OR (user_id IS NULL AND group_id IS NOT NULL))
);
CREATE UNIQUE INDEX security_read_grants_user_unique
    ON security_read_grants(repo_key, user_id)
    WHERE user_id IS NOT NULL;
CREATE UNIQUE INDEX security_read_grants_group_unique
    ON security_read_grants(repo_key, group_id)
    WHERE group_id IS NOT NULL;
