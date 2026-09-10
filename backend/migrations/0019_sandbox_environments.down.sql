ALTER TABLE users DROP COLUMN sandbox_intro_seen_at;
ALTER TABLE sessions DROP FOREIGN KEY fk_sessions_sandbox, DROP KEY idx_sessions_sandbox,
    DROP COLUMN sandbox_environment_id;
DROP TABLE sandbox_environments;
