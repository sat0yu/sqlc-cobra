-- name: CountProjects :one
SELECT COUNT(*) FROM projects;

-- name: GetProject :one
SELECT id, name, owner_id, created_at FROM projects WHERE id = ?;

-- name: ListProjects :many
SELECT id, name, owner_id, created_at FROM projects;

-- name: CreateProject :execresult
INSERT INTO projects (id, name, owner_id) VALUES (?, ?, ?);

-- name: DeleteProject :exec
DELETE FROM projects WHERE id = ?;

-- name: CountDevices :one
SELECT COUNT(*) FROM devices;

-- name: GetDevice :one
SELECT id, project_id, name, created_at FROM devices WHERE id = ?;

-- name: ListDevices :many
SELECT id, project_id, name, created_at FROM devices WHERE project_id = ?;

-- name: CreateDevice :execresult
INSERT INTO devices (id, project_id, name) VALUES (?, ?, ?);

-- name: DeleteDevice :exec
DELETE FROM devices WHERE id = ?;
