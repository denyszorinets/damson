-- name: InsertPluginManifest :exec
INSERT INTO plugin_manifests (name, version, checksum, manifest, created_at)
VALUES (sqlc.arg(name), sqlc.arg(version), sqlc.arg(checksum), sqlc.arg(manifest), sqlc.arg(created_at))
ON CONFLICT (name, version, checksum) DO NOTHING;

-- name: DeleteAgentPlugins :exec
DELETE FROM agent_plugins WHERE agent_id = sqlc.arg(agent_id);

-- name: InsertAgentPlugin :exec
INSERT INTO agent_plugins (agent_id, name, version, checksum)
VALUES (sqlc.arg(agent_id), sqlc.arg(name), sqlc.arg(version), sqlc.arg(checksum));

-- Providers whose agent is connected (owned by a controller, not
-- disconnected) and was seen recently: a crashed controller's agents age out.
-- name: ListLivePluginProviders :many
SELECT ap.agent_id, p.name AS pool, a.controller_id, a.capabilities, ap.name, ap.version, ap.checksum
FROM agent_plugins ap
JOIN agents a ON a.id = ap.agent_id
JOIN agent_pools p ON p.id = a.pool_id
WHERE a.controller_id IS NOT NULL AND a.disconnected_at IS NULL AND a.last_seen >= sqlc.arg(seen_after)
ORDER BY ap.agent_id, ap.name, ap.version;

-- name: ListPluginManifests :many
SELECT name, version, checksum, manifest FROM plugin_manifests ORDER BY name, version, checksum;

-- name: DeleteOrphanPluginManifests :execrows
DELETE FROM plugin_manifests
WHERE created_at < sqlc.arg(older_than)
  AND NOT EXISTS (SELECT 1 FROM agent_plugins ap WHERE ap.name = plugin_manifests.name
                  AND ap.version = plugin_manifests.version AND ap.checksum = plugin_manifests.checksum);
