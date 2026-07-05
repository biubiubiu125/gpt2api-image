package app

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

const (
	dbTaskStatusQueued   = "queued"
	dbTaskStatusRunning  = "running"
	dbTaskStatusSuccess  = "success"
	dbTaskStatusError    = "error"
	dbTaskStatusCanceled = "canceled"
)

var errImageAccountBusy = errors.New("image account concurrency limit reached")
var errAdminClientTaskOwnerRequired = errors.New("owner_id is required when admin uses client_task_id")

type PGTaskStore struct {
	db *sql.DB
}

type DBImageTask struct {
	ID             string
	ClientTaskID   string
	OwnerID        string
	OwnerRole      string
	Status         string
	Mode           string
	Prompt         string
	Model          string
	Size           string
	Resolution     string
	ResponseFormat string
	N              int
	InputPaths     []string
	ResultData     []map[string]any
	Error          string
	CallbackURL    string
	DeadlineTS     float64
	CreatedAt      string
	UpdatedAt      string
}

func NewPGTaskStore(databaseURL string) (*PGTaskStore, error) {
	db, err := sql.Open("pgx", strings.TrimSpace(databaseURL))
	if err != nil {
		return nil, err
	}
	maxOpen := envInt("GPT2API_IMAGE_DB_MAX_OPEN_CONNS", 20)
	if maxOpen < 1 {
		maxOpen = 1
	}
	maxIdle := envInt("GPT2API_IMAGE_DB_MAX_IDLE_CONNS", 10)
	if maxIdle < 0 {
		maxIdle = 0
	}
	if maxIdle > maxOpen {
		maxIdle = maxOpen
	}
	db.SetMaxOpenConns(maxOpen)
	db.SetMaxIdleConns(maxIdle)
	db.SetConnMaxLifetime(30 * time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	store := &PGTaskStore{db: db}
	if err := store.ensureSchema(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *PGTaskStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *PGTaskStore) ensureSchema(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS image_tasks_v3 (
  id TEXT PRIMARY KEY,
  client_task_id TEXT NOT NULL DEFAULT '',
  owner_id TEXT NOT NULL,
  owner_role TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL,
  mode TEXT NOT NULL,
  prompt TEXT NOT NULL,
  model TEXT NOT NULL DEFAULT 'gpt-image-2',
  size TEXT NOT NULL DEFAULT '',
  resolution TEXT NOT NULL DEFAULT '',
  response_format TEXT NOT NULL DEFAULT 'url',
  n INTEGER NOT NULL DEFAULT 1,
  input_paths TEXT NOT NULL DEFAULT '[]',
  result_data TEXT NOT NULL DEFAULT '[]',
  error TEXT NOT NULL DEFAULT '',
  callback_url TEXT NOT NULL DEFAULT '',
  claimed_by TEXT NOT NULL DEFAULT '',
  claimed_until_ts DOUBLE PRECISION NOT NULL DEFAULT 0,
  deadline_ts DOUBLE PRECISION NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  created_ts DOUBLE PRECISION NOT NULL,
  updated_ts DOUBLE PRECISION NOT NULL
);
ALTER TABLE image_tasks_v3 ADD COLUMN IF NOT EXISTS client_task_id TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS ix_image_tasks_v3_claim ON image_tasks_v3(status, claimed_until_ts, created_ts);
CREATE INDEX IF NOT EXISTS ix_image_tasks_v3_owner ON image_tasks_v3(owner_id, created_ts DESC);
CREATE INDEX IF NOT EXISTS ix_image_tasks_v3_client_task ON image_tasks_v3(client_task_id, owner_id);

CREATE TABLE IF NOT EXISTS account_leases_v1 (
  lease_id TEXT PRIMARY KEY,
  token_hash TEXT NOT NULL,
  holder TEXT NOT NULL,
  expires_ts DOUBLE PRECISION NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_account_leases_v1_token ON account_leases_v1(token_hash, expires_ts);
`)
	return err
}

func (s *PGTaskStore) CreateTask(ctx context.Context, task DBImageTask) (DBImageTask, bool, error) {
	if strings.TrimSpace(task.ID) == "" {
		task.ID = randID(16)
	}
	if strings.TrimSpace(task.OwnerID) == "" {
		return DBImageTask{}, false, errors.New("owner_id is required")
	}
	if strings.TrimSpace(task.Prompt) == "" {
		return DBImageTask{}, false, errors.New("prompt is required")
	}
	if task.N < 1 {
		task.N = 1
	}
	if task.N > 4 {
		task.N = 4
	}
	task.Model = normalizeImageModel(task.Model)
	if task.ResponseFormat == "" {
		task.ResponseFormat = "url"
	}
	if task.Status == "" {
		task.Status = dbTaskStatusQueued
	}
	now := nowISO()
	nowTS := float64(time.Now().UnixNano()) / 1e9
	if task.CreatedAt == "" {
		task.CreatedAt = now
	}
	task.UpdatedAt = now
	if task.DeadlineTS <= 0 {
		task.DeadlineTS = nowTS + 300
	}
	inputRaw, _ := json.Marshal(task.InputPaths)
	resultRaw, _ := json.Marshal(task.ResultData)
	result, err := s.db.ExecContext(ctx, `
INSERT INTO image_tasks_v3 (
  id, client_task_id, owner_id, owner_role, status, mode, prompt, model, size, resolution,
  response_format, n, input_paths, result_data, error, callback_url,
  claimed_by, claimed_until_ts, deadline_ts, created_at, updated_at, created_ts, updated_ts
) VALUES (
  $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,'',0,$17,$18,$19,$20,$21
)
ON CONFLICT (id) DO NOTHING
`, task.ID, task.ClientTaskID, task.OwnerID, task.OwnerRole, task.Status, task.Mode, task.Prompt, task.Model, task.Size, task.Resolution,
		task.ResponseFormat, task.N, string(inputRaw), string(resultRaw), task.Error, task.CallbackURL,
		task.DeadlineTS, task.CreatedAt, task.UpdatedAt, nowTS, nowTS)
	if err != nil {
		return DBImageTask{}, false, err
	}
	created, _ := result.RowsAffected()
	item, err := s.GetTask(ctx, task.ID, Identity{ID: task.OwnerID, Role: task.OwnerRole})
	if err != nil && created > 0 {
		return task, true, nil
	}
	return item, created > 0, err
}

func (s *PGTaskStore) GetTask(ctx context.Context, id string, identity Identity) (DBImageTask, error) {
	query := `SELECT id, client_task_id, owner_id, owner_role, status, mode, prompt, model, size, resolution, response_format, n, input_paths, result_data, error, callback_url, deadline_ts, created_at, updated_at FROM image_tasks_v3 WHERE id=$1`
	args := []any{id}
	if identity.Role != "admin" {
		query += ` AND owner_id=$2`
		args = append(args, identity.ID)
	}
	row := s.db.QueryRowContext(ctx, query, args...)
	return scanDBTask(row)
}

func (s *PGTaskStore) CountTasks(ctx context.Context) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM image_tasks_v3`).Scan(&count)
	return count, err
}

func (s *PGTaskStore) ListTasks(ctx context.Context, identity Identity, ids []string) ([]DBImageTask, []string, error) {
	items := []DBImageTask{}
	missing := []string{}
	if len(ids) > 0 {
		for _, id := range ids {
			id = strings.TrimSpace(id)
			if id == "" {
				continue
			}
			var task DBImageTask
			var err error
			found := false
			for _, candidate := range possibleImageTaskIDs(identity.ID, id) {
				task, err = s.GetTask(ctx, candidate, identity)
				if err == nil {
					found = true
					break
				}
				if !errors.Is(err, sql.ErrNoRows) {
					return nil, nil, err
				}
			}
			if !found && canUseClientTaskIDFallback(identity) {
				matches, err := s.TasksByClientTaskID(ctx, id, identity)
				if err != nil {
					return nil, nil, err
				}
				if len(matches) > 0 {
					items = append(items, matches...)
					found = true
				}
			}
			if !found {
				missing = append(missing, id)
				continue
			}
			if task.ID != "" {
				items = append(items, task)
			}
		}
		return items, missing, nil
	}
	query := `SELECT id, client_task_id, owner_id, owner_role, status, mode, prompt, model, size, resolution, response_format, n, input_paths, result_data, error, callback_url, deadline_ts, created_at, updated_at FROM image_tasks_v3`
	args := []any{}
	if identity.Role != "admin" {
		query += ` WHERE owner_id=$1`
		args = append(args, identity.ID)
	}
	query += ` ORDER BY created_ts DESC LIMIT 200`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		task, err := scanDBTask(rows)
		if err != nil {
			return nil, nil, err
		}
		items = append(items, task)
	}
	return items, missing, rows.Err()
}

func (s *PGTaskStore) TasksByClientTaskID(ctx context.Context, clientTaskID string, identity Identity) ([]DBImageTask, error) {
	clientTaskID = strings.TrimSpace(clientTaskID)
	if clientTaskID == "" {
		return nil, nil
	}
	if identity.Role == "admin" {
		return nil, errAdminClientTaskOwnerRequired
	}
	query := `SELECT id, client_task_id, owner_id, owner_role, status, mode, prompt, model, size, resolution, response_format, n, input_paths, result_data, error, callback_url, deadline_ts, created_at, updated_at FROM image_tasks_v3 WHERE client_task_id=$1`
	args := []any{clientTaskID}
	if identity.Role != "admin" {
		query += ` AND owner_id=$2`
		args = append(args, identity.ID)
	}
	query += ` ORDER BY created_ts DESC LIMIT 50`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []DBImageTask{}
	for rows.Next() {
		task, err := scanDBTask(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, task)
	}
	return items, rows.Err()
}

func (s *PGTaskStore) ClaimTask(ctx context.Context, workerID string, ttl time.Duration) (DBImageTask, bool, error) {
	nowTS := float64(time.Now().UnixNano()) / 1e9
	claimedUntil := nowTS + ttl.Seconds()
	row := s.db.QueryRowContext(ctx, `
WITH candidate AS (
  SELECT id
  FROM image_tasks_v3
  WHERE status IN ($1, $2)
    AND deadline_ts > $3
    AND (
      (status=$1 AND claimed_until_ts <= $3) OR
      (status=$2 AND (claimed_by='' OR claimed_until_ts <= $3))
    )
  ORDER BY created_ts ASC
  FOR UPDATE SKIP LOCKED
  LIMIT 1
)
UPDATE image_tasks_v3 t
SET status=$2, claimed_by=$4, claimed_until_ts=$5, updated_at=$6, updated_ts=$3
FROM candidate
WHERE t.id=candidate.id
RETURNING t.id, t.client_task_id, t.owner_id, t.owner_role, t.status, t.mode, t.prompt, t.model, t.size, t.resolution, t.response_format, t.n, t.input_paths, t.result_data, t.error, t.callback_url, t.deadline_ts, t.created_at, t.updated_at
`, dbTaskStatusQueued, dbTaskStatusRunning, nowTS, workerID, claimedUntil, nowISO())
	task, err := scanDBTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return DBImageTask{}, false, nil
	}
	if err != nil {
		return DBImageTask{}, false, err
	}
	return task, true, nil
}

func (s *PGTaskStore) ClaimTaskByID(ctx context.Context, id string, workerID string, ttl time.Duration) (DBImageTask, bool, error) {
	nowTS := float64(time.Now().UnixNano()) / 1e9
	claimedUntil := nowTS + ttl.Seconds()
	row := s.db.QueryRowContext(ctx, `
UPDATE image_tasks_v3
SET status=$1, claimed_by=$2, claimed_until_ts=$3, updated_at=$4, updated_ts=$5
WHERE id=$6 AND status=$7 AND deadline_ts > $5
RETURNING id, client_task_id, owner_id, owner_role, status, mode, prompt, model, size, resolution, response_format, n, input_paths, result_data, error, callback_url, deadline_ts, created_at, updated_at
`, dbTaskStatusRunning, workerID, claimedUntil, nowISO(), nowTS, id, dbTaskStatusQueued)
	task, err := scanDBTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return DBImageTask{}, false, nil
	}
	if err != nil {
		return DBImageTask{}, false, err
	}
	return task, true, nil
}

func (s *PGTaskStore) HeartbeatTask(ctx context.Context, id string, workerID string, ttl time.Duration) (bool, error) {
	nowTS := float64(time.Now().UnixNano()) / 1e9
	result, err := s.db.ExecContext(ctx, `
UPDATE image_tasks_v3
SET claimed_until_ts=$1, updated_at=$2, updated_ts=$3
WHERE id=$4 AND status=$5 AND claimed_by=$6
`, nowTS+ttl.Seconds(), nowISO(), nowTS, id, dbTaskStatusRunning, workerID)
	if err != nil {
		return false, err
	}
	count, _ := result.RowsAffected()
	return count > 0, nil
}

func (s *PGTaskStore) CompleteTask(ctx context.Context, id string, workerID string, data []map[string]any) (bool, error) {
	raw, _ := json.Marshal(data)
	result, err := s.db.ExecContext(ctx, `
UPDATE image_tasks_v3
SET status=$1, result_data=$2, error='', claimed_by='', claimed_until_ts=0, updated_at=$3, updated_ts=$4
WHERE id=$5 AND status=$6 AND claimed_by=$7
`, dbTaskStatusSuccess, string(raw), nowISO(), float64(time.Now().UnixNano())/1e9, id, dbTaskStatusRunning, workerID)
	if err != nil {
		return false, err
	}
	count, _ := result.RowsAffected()
	return count > 0, nil
}

func (s *PGTaskStore) FailTask(ctx context.Context, id string, workerID string, message string) (bool, error) {
	query := `
UPDATE image_tasks_v3
SET status=$1, error=$2, claimed_by='', claimed_until_ts=0, updated_at=$3, updated_ts=$4
WHERE id=$5 AND status IN ($6, $7)
`
	args := []any{dbTaskStatusError, limitTaskError(message, 4000), nowISO(), float64(time.Now().UnixNano()) / 1e9, id, dbTaskStatusQueued, dbTaskStatusRunning}
	if strings.TrimSpace(workerID) != "" {
		query += ` AND claimed_by=$8`
		args = append(args, workerID)
	}
	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return false, err
	}
	count, _ := result.RowsAffected()
	return count > 0, nil
}

func (s *PGTaskStore) RequeueTask(ctx context.Context, id string, workerID string, delay time.Duration) (bool, error) {
	nowTS := float64(time.Now().UnixNano()) / 1e9
	result, err := s.db.ExecContext(ctx, `
UPDATE image_tasks_v3
SET status=$1, claimed_by='', claimed_until_ts=$2, updated_at=$3, updated_ts=$4
WHERE id=$5 AND status=$6 AND claimed_by=$7
`, dbTaskStatusQueued, nowTS+delay.Seconds(), nowISO(), nowTS, id, dbTaskStatusRunning, workerID)
	if err != nil {
		return false, err
	}
	count, _ := result.RowsAffected()
	return count > 0, nil
}

func (s *PGTaskStore) CancelTask(ctx context.Context, id string, identity Identity) (DBImageTask, bool, error) {
	query := `
UPDATE image_tasks_v3
SET status=$1, error='canceled', claimed_by='', claimed_until_ts=0, updated_at=$2, updated_ts=$3
WHERE id=$4 AND status IN ($5,$6)
`
	args := []any{dbTaskStatusCanceled, nowISO(), float64(time.Now().UnixNano()) / 1e9, id, dbTaskStatusQueued, dbTaskStatusRunning}
	if identity.Role != "admin" {
		query += ` AND owner_id=$7`
		args = append(args, identity.ID)
	}
	query += ` RETURNING id, client_task_id, owner_id, owner_role, status, mode, prompt, model, size, resolution, response_format, n, input_paths, result_data, error, callback_url, deadline_ts, created_at, updated_at`
	task, err := scanDBTask(s.db.QueryRowContext(ctx, query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return DBImageTask{}, false, nil
	}
	if err != nil {
		return DBImageTask{}, false, err
	}
	return task, true, nil
}

func canUseClientTaskIDFallback(identity Identity) bool {
	return identity.Role != "admin"
}

func (s *PGTaskStore) CancelTasksByClientTaskID(ctx context.Context, clientTaskID string, identity Identity) ([]DBImageTask, error) {
	clientTaskID = strings.TrimSpace(clientTaskID)
	if clientTaskID == "" {
		return nil, nil
	}
	if identity.Role == "admin" {
		return nil, errAdminClientTaskOwnerRequired
	}
	query := `
UPDATE image_tasks_v3
SET status=$1, error='canceled', claimed_by='', claimed_until_ts=0, updated_at=$2, updated_ts=$3
WHERE client_task_id=$4 AND status IN ($5,$6)
`
	args := []any{dbTaskStatusCanceled, nowISO(), float64(time.Now().UnixNano()) / 1e9, clientTaskID, dbTaskStatusQueued, dbTaskStatusRunning}
	if identity.Role != "admin" {
		query += ` AND owner_id=$7`
		args = append(args, identity.ID)
	}
	query += ` RETURNING id, client_task_id, owner_id, owner_role, status, mode, prompt, model, size, resolution, response_format, n, input_paths, result_data, error, callback_url, deadline_ts, created_at, updated_at`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []DBImageTask{}
	for rows.Next() {
		task, err := scanDBTask(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, task)
	}
	return items, rows.Err()
}

func (s *PGTaskStore) ExpireOverdueTasks(ctx context.Context, limit int) ([]DBImageTask, error) {
	if limit <= 0 {
		limit = 50
	}
	nowTS := float64(time.Now().UnixNano()) / 1e9
	rows, err := s.db.QueryContext(ctx, `
WITH candidate AS (
  SELECT id
  FROM image_tasks_v3
  WHERE status IN ($1,$2) AND deadline_ts > 0 AND deadline_ts <= $3
  ORDER BY created_ts ASC
  FOR UPDATE SKIP LOCKED
  LIMIT $4
)
UPDATE image_tasks_v3 t
SET status=$5, error='image task timed out', claimed_by='', claimed_until_ts=0, updated_at=$6, updated_ts=$3
FROM candidate
WHERE t.id=candidate.id
RETURNING t.id, t.client_task_id, t.owner_id, t.owner_role, t.status, t.mode, t.prompt, t.model, t.size, t.resolution, t.response_format, t.n, t.input_paths, t.result_data, t.error, t.callback_url, t.deadline_ts, t.created_at, t.updated_at
`, dbTaskStatusQueued, dbTaskStatusRunning, nowTS, limit, dbTaskStatusError, nowISO())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []DBImageTask{}
	for rows.Next() {
		task, err := scanDBTask(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, task)
	}
	return items, rows.Err()
}

func (s *PGTaskStore) DeleteFinishedTasksBefore(ctx context.Context, cutoff time.Time, limit int) ([]DBImageTask, error) {
	if limit <= 0 {
		limit = 200
	}
	cutoffTS := float64(cutoff.UnixNano()) / 1e9
	rows, err := s.db.QueryContext(ctx, `
WITH candidate AS (
  SELECT id
  FROM image_tasks_v3
  WHERE status IN ($1,$2,$3) AND updated_ts <= $4
  ORDER BY updated_ts ASC
  FOR UPDATE SKIP LOCKED
  LIMIT $5
)
DELETE FROM image_tasks_v3 t
USING candidate
WHERE t.id=candidate.id
RETURNING t.id, t.client_task_id, t.owner_id, t.owner_role, t.status, t.mode, t.prompt, t.model, t.size, t.resolution, t.response_format, t.n, t.input_paths, t.result_data, t.error, t.callback_url, t.deadline_ts, t.created_at, t.updated_at
`, dbTaskStatusSuccess, dbTaskStatusError, dbTaskStatusCanceled, cutoffTS, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []DBImageTask{}
	for rows.Next() {
		task, err := scanDBTask(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, task)
	}
	return items, rows.Err()
}

func (s *PGTaskStore) AcquireAccountLease(ctx context.Context, token string, maxLeases int, holder string, ttl time.Duration) (string, bool, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return "", false, errors.New("account token is required")
	}
	if maxLeases < 1 {
		maxLeases = 1
	}
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	now := time.Now()
	nowTS := float64(now.UnixNano()) / 1e9
	expiresTS := nowTS + ttl.Seconds()
	sum := sha256.Sum256([]byte(token))
	tokenHash := hex.EncodeToString(sum[:])
	leaseID := randID(16)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `LOCK TABLE account_leases_v1 IN EXCLUSIVE MODE`); err != nil {
		return "", false, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM account_leases_v1 WHERE expires_ts <= $1`, nowTS); err != nil {
		return "", false, err
	}
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM account_leases_v1 WHERE token_hash=$1 AND expires_ts > $2`, tokenHash, nowTS).Scan(&active); err != nil {
		return "", false, err
	}
	if active >= maxLeases {
		return "", false, nil
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO account_leases_v1 (lease_id, token_hash, holder, expires_ts, created_at, updated_at)
VALUES ($1,$2,$3,$4,$5,$5)
`, leaseID, tokenHash, holder, expiresTS, nowISO()); err != nil {
		return "", false, err
	}
	if err := tx.Commit(); err != nil {
		return "", false, err
	}
	return leaseID, true, nil
}

func (s *PGTaskStore) ReleaseAccountLease(ctx context.Context, leaseID string) error {
	leaseID = strings.TrimSpace(leaseID)
	if leaseID == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM account_leases_v1 WHERE lease_id=$1`, leaseID)
	return err
}

func (s *PGTaskStore) CountAccountLeases(ctx context.Context, token string) (int, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return 0, nil
	}
	nowTS := float64(time.Now().UnixNano()) / 1e9
	sum := sha256.Sum256([]byte(token))
	tokenHash := hex.EncodeToString(sum[:])
	if _, err := s.db.ExecContext(ctx, `DELETE FROM account_leases_v1 WHERE expires_ts <= $1`, nowTS); err != nil {
		return 0, err
	}
	var active int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM account_leases_v1 WHERE token_hash=$1 AND expires_ts > $2`, tokenHash, nowTS).Scan(&active); err != nil {
		return 0, err
	}
	return active, nil
}

func limitTaskError(value string, maxLen int) string {
	value = strings.TrimSpace(value)
	if maxLen <= 0 || len(value) <= maxLen {
		return value
	}
	return value[:maxLen]
}

func scanDBTask(scanner interface {
	Scan(dest ...any) error
}) (DBImageTask, error) {
	var task DBImageTask
	var inputsRaw, resultRaw string
	err := scanner.Scan(
		&task.ID, &task.ClientTaskID, &task.OwnerID, &task.OwnerRole, &task.Status, &task.Mode, &task.Prompt,
		&task.Model, &task.Size, &task.Resolution, &task.ResponseFormat, &task.N,
		&inputsRaw, &resultRaw, &task.Error, &task.CallbackURL, &task.DeadlineTS,
		&task.CreatedAt, &task.UpdatedAt,
	)
	if err != nil {
		return DBImageTask{}, err
	}
	_ = json.Unmarshal([]byte(inputsRaw), &task.InputPaths)
	_ = json.Unmarshal([]byte(resultRaw), &task.ResultData)
	return task, nil
}

func (t DBImageTask) Public() ImageTask {
	return ImageTask{
		ID:           t.ID,
		ClientTaskID: t.ClientTaskID,
		OwnerID:      t.OwnerID,
		Status:       t.Status,
		Mode:         t.Mode,
		Model:        normalizeImageModel(t.Model),
		Size:         t.Size,
		Resolution:   t.Resolution,
		CreatedAt:    t.CreatedAt,
		UpdatedAt:    t.UpdatedAt,
		Data:         t.ResultData,
		Error:        t.Error,
	}
}
