ALTER TABLE admin_messages
    DROP KEY idx_admin_messages_assignee,
    DROP COLUMN assigned_to_username,
    DROP COLUMN assigned_to_user_id;
