-- name: GetSetting :one
SELECT setting_key, setting_value, version
FROM instance_settings
WHERE setting_key = sqlc.arg(setting_key);

-- name: InsertSetting :execrows
INSERT INTO instance_settings (setting_key, setting_value)
VALUES (sqlc.arg(setting_key), sqlc.arg(setting_value));

-- name: UpdateSettingCAS :execrows
UPDATE instance_settings
SET setting_value = sqlc.arg(setting_value), version = version + 1
WHERE setting_key = sqlc.arg(setting_key) AND version = sqlc.arg(expected_version);
