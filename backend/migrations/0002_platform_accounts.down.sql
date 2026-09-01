DROP TABLE IF EXISTS password_reset_challenges;
ALTER TABLE users
    DROP KEY uq_users_platform_username,
    DROP COLUMN platform_username,
    DROP COLUMN contact_email;
