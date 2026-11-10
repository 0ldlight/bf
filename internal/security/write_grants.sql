-- Optional write-grant extension to security schema v1.
-- Apply once in an operator-owned transaction. No grants are seeded.
CREATE TABLE security_write_grants (
    repo_key varchar(64) NOT NULL REFERENCES security_repositories(repo_key) ON DELETE CASCADE,
    user_id bigint REFERENCES security_users(user_id) ON DELETE CASCADE,
    group_id bigint REFERENCES security_groups(group_id) ON DELETE CASCADE,
    action text NOT NULL CHECK (action IN ('deploy', 'delete')),
    scope text NOT NULL CHECK (scope = '**'),
    CHECK ((user_id IS NULL) <> (group_id IS NULL))
);
CREATE UNIQUE INDEX security_write_grants_user_unique
    ON security_write_grants(repo_key, action, user_id) WHERE user_id IS NOT NULL;
CREATE UNIQUE INDEX security_write_grants_group_unique
    ON security_write_grants(repo_key, action, group_id) WHERE group_id IS NOT NULL;
