package security

import _ "embed"

// SchemaSQL 是 BIN-201 安全基础 schema v1 的全文（见 schema.sql 的双语
// 头注）：版本表 + 用户/组/成员/仓库/仓库级 read 授权。仅供可信运维/测试
// 在自有事务中显式应用；Store/启动/Validate 从不执行任何 DDL，也不自动
// 创建管理员或授权。
// SchemaSQL embeds the full BIN-201 security foundation schema v1 (see the
// bilingual header of schema.sql): the version table plus users, groups,
// memberships, repositories and repository-wide read grants. Trusted
// operators/tests apply it explicitly inside their own transaction; the
// Store/startup/Validate never execute DDL and create no implicit admin or
// grant.
//
//go:embed schema.sql
var SchemaSQL string
