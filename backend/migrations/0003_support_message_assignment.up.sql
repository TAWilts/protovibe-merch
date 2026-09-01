ALTER TABLE admin_messages
    ADD COLUMN assigned_to_user_id BIGINT NULL AFTER body,
    ADD COLUMN assigned_to_username VARCHAR(150) NOT NULL DEFAULT '' AFTER assigned_to_user_id,
    ADD KEY idx_admin_messages_assignee (assigned_to_user_id, is_resolved, created_at);
