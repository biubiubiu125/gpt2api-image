package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPGTaskStoreIntegrationAsyncLifecycle(t *testing.T) {
	databaseURL := strings.TrimSpace(os.Getenv("GPT2API_IMAGE_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("set GPT2API_IMAGE_TEST_DATABASE_URL to run PostgreSQL task-store integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	store, err := NewPGTaskStore(databaseURL)
	if err != nil {
		t.Fatalf("open pg task store: %v", err)
	}
	defer store.db.Close()

	suffix := randID(10)
	owner := Identity{ID: "owner_" + suffix, Role: "user"}
	admin := Identity{ID: "admin_" + suffix, Role: "admin"}
	taskID := "task_" + suffix
	clientTaskID := "client_" + suffix
	workerTaskID := "task_worker_" + suffix
	workerClientTaskID := "client_worker_" + suffix
	workerFailTaskID := "task_worker_fail_" + suffix
	workerFailClientTaskID := "client_worker_fail_" + suffix
	cancelTaskID := "task_cancel_" + suffix
	cancelClientTaskID := "client_cancel_" + suffix
	defer func() {
		_, _ = store.db.ExecContext(context.Background(), `DELETE FROM image_tasks_v3 WHERE owner_id=$1 OR id IN ($2,$3,$4,$5)`, owner.ID, taskID, workerTaskID, workerFailTaskID, cancelTaskID)
	}()

	task, created, err := store.CreateTask(ctx, DBImageTask{
		ID:             taskID,
		ClientTaskID:   clientTaskID,
		OwnerID:        owner.ID,
		OwnerRole:      owner.Role,
		Status:         dbTaskStatusQueued,
		Mode:           "generate",
		Prompt:         "integration image prompt",
		Model:          "gpt-image-2",
		ResponseFormat: "b64_json",
		N:              2,
		InputPaths:     []string{"/tmp/gpt2api-image-input"},
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if !created || task.ID != taskID || task.ClientTaskID != clientTaskID {
		t.Fatalf("created task = %#v created=%v", task, created)
	}

	listed, missing, err := store.ListTasks(ctx, owner, []string{clientTaskID})
	if err != nil {
		t.Fatalf("list by client_task_id: %v", err)
	}
	if len(missing) != 0 || len(listed) != 1 || listed[0].ID != taskID {
		t.Fatalf("list by client_task_id listed=%#v missing=%#v", listed, missing)
	}

	listed, missing, err = store.ListTasks(ctx, admin, []string{"missing_" + suffix})
	if err != nil {
		t.Fatalf("admin missing id should not require owner_id: %v", err)
	}
	if len(listed) != 0 || len(missing) != 1 {
		t.Fatalf("admin missing id listed=%#v missing=%#v", listed, missing)
	}

	claimed, ok, err := store.ClaimTaskByID(ctx, taskID, "worker_"+suffix, time.Minute)
	if err != nil {
		t.Fatalf("claim task: %v", err)
	}
	if !ok || claimed.ID != taskID || claimed.Status != dbTaskStatusRunning {
		t.Fatalf("claimed task = %#v ok=%v", claimed, ok)
	}

	if ok, err := store.HeartbeatTask(ctx, taskID, "worker_"+suffix, time.Minute); err != nil || !ok {
		t.Fatalf("heartbeat ok=%v err=%v", ok, err)
	}
	if ok, err := store.CompleteTask(ctx, taskID, "worker_"+suffix, []map[string]any{{"url": "https://example.com/image.png"}}); err != nil || !ok {
		t.Fatalf("complete ok=%v err=%v", ok, err)
	}
	completed, err := store.GetTask(ctx, taskID, owner)
	if err != nil {
		t.Fatalf("get completed task: %v", err)
	}
	if completed.Status != dbTaskStatusSuccess || len(completed.ResultData) != 1 {
		t.Fatalf("completed task = %#v", completed)
	}
	oldUpdatedTS := float64(time.Now().AddDate(0, 0, -30).UnixNano()) / 1e9
	if _, err := store.db.ExecContext(ctx, `UPDATE image_tasks_v3 SET updated_ts=$1 WHERE id=$2`, oldUpdatedTS, taskID); err != nil {
		t.Fatalf("age completed task: %v", err)
	}
	deleted, err := store.DeleteFinishedTasksBefore(ctx, time.Now().AddDate(0, 0, -15), 10)
	if err != nil {
		t.Fatalf("delete finished old tasks: %v", err)
	}
	foundDeleted := false
	for _, item := range deleted {
		if item.ID == taskID {
			foundDeleted = true
			break
		}
	}
	if !foundDeleted {
		t.Fatalf("deleted old tasks = %#v, want %s", deleted, taskID)
	}

	dataDir := filepath.Join(t.TempDir(), "data")
	cfg := Config{
		BaseURL:                     "https://example.com",
		ImageRetentionDays:          15,
		ImagePollTimeoutSecs:        1,
		ImageTaskClaimTTLSecs:       60,
		ImageWorkerPollIntervalSecs: 1,
		ImageAccountConcurrency:     1,
	}
	workerServer := &Server{
		dataDir:    dataDir,
		imagesDir:  filepath.Join(dataDir, "images"),
		cfg:        cfg,
		store:      NewStore(dataDir),
		taskStore:  store,
		logSvc:     newLogService(dataDir),
		callStarts: map[string]time.Time{},
	}
	inputPaths, err := workerServer.saveTaskInputImages(workerTaskID, [][]byte{[]byte("input-image")})
	if err != nil {
		t.Fatalf("save worker task input: %v", err)
	}
	if len(inputPaths) != 1 {
		t.Fatalf("worker task input paths = %#v, want one path", inputPaths)
	}
	generatorCalled := false
	workerServer.imageGenerator = func(ctx context.Context, prompt, model, size, resolution string, refs [][]byte, n int) ([]upstreamImageResult, error) {
		generatorCalled = true
		if prompt != "worker edit prompt" || model != "gpt-image-2" || size != "1:1" || resolution != "1k" || n != 1 {
			t.Fatalf("worker generator args prompt=%q model=%q size=%q resolution=%q n=%d", prompt, model, size, resolution, n)
		}
		if len(refs) != 1 || string(refs[0]) != "input-image" {
			t.Fatalf("worker generator refs = %#v", refs)
		}
		return []upstreamImageResult{{Bytes: []byte("generated-image"), RevisedPrompt: "worker revised prompt"}}, nil
	}
	_, created, err = store.CreateTask(ctx, DBImageTask{
		ID:             workerTaskID,
		ClientTaskID:   workerClientTaskID,
		OwnerID:        owner.ID,
		OwnerRole:      owner.Role,
		Status:         dbTaskStatusQueued,
		Mode:           "edit",
		Prompt:         "worker edit prompt",
		Model:          "gpt-image-2",
		Size:           "1:1",
		Resolution:     "1k",
		ResponseFormat: "b64_json",
		N:              1,
		InputPaths:     inputPaths,
	})
	if err != nil {
		t.Fatalf("create worker task: %v", err)
	}
	if !created {
		t.Fatalf("worker task was not created")
	}
	workerID := "worker_full_" + suffix
	workerClaimed, ok, err := store.ClaimTaskByID(ctx, workerTaskID, workerID, time.Minute)
	if err != nil {
		t.Fatalf("claim worker task: %v", err)
	}
	if !ok {
		t.Fatalf("worker task was not claimed")
	}
	if err := workerServer.runDBImageTask(ctx, workerID, time.Minute, workerClaimed); err != nil {
		t.Fatalf("run worker image task: %v", err)
	}
	if !generatorCalled {
		t.Fatalf("worker generator was not called")
	}
	workerCompleted, err := store.GetTask(ctx, workerTaskID, owner)
	if err != nil {
		t.Fatalf("get worker completed task: %v", err)
	}
	if workerCompleted.Status != dbTaskStatusSuccess || len(workerCompleted.ResultData) != 1 {
		t.Fatalf("worker completed task = %#v", workerCompleted)
	}
	result := workerCompleted.ResultData[0]
	url := strings.TrimSpace(strAny(result["url"], ""))
	if !strings.HasPrefix(url, "https://example.com/images/") {
		t.Fatalf("worker result url = %q", url)
	}
	if strings.TrimSpace(strAny(result["b64_json"], "")) == "" {
		t.Fatalf("worker result missing b64_json: %#v", result)
	}
	rel := strings.TrimPrefix(url, "https://example.com/images/")
	if _, err := os.Stat(filepath.Join(workerServer.imagesDir, filepath.FromSlash(rel))); err != nil {
		t.Fatalf("worker saved image missing: %v", err)
	}
	if got := workerServer.store.LoadOwners()[rel]; got != owner.ID {
		t.Fatalf("worker saved owner = %q, want %q", got, owner.ID)
	}
	promptMeta := workerServer.store.LoadPrompts()[rel]
	if strings.TrimSpace(strAny(promptMeta["prompt"], "")) != "worker edit prompt" || !boolAny(promptMeta["is_edit"], false) {
		t.Fatalf("worker prompt metadata = %#v", promptMeta)
	}
	for _, path := range inputPaths {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("worker input path should be cleaned: %s stat=%v", path, err)
		}
	}

	if err := workerServer.store.SaveAuthKeys([]UserKey{{
		ID:                  owner.ID,
		Name:                "integration owner",
		Role:                owner.Role,
		Enabled:             true,
		ImageDailyUsed:      1,
		ImageMonthlyUsed:    1,
		ImageTotalUsed:      1,
		ImageDailyUnlimited: true,
	}}); err != nil {
		t.Fatalf("save worker refund auth key: %v", err)
	}
	failInputPaths, err := workerServer.saveTaskInputImages(workerFailTaskID, [][]byte{[]byte("fail-input-image")})
	if err != nil {
		t.Fatalf("save worker fail task input: %v", err)
	}
	workerServer.imageGenerator = func(ctx context.Context, prompt, model, size, resolution string, refs [][]byte, n int) ([]upstreamImageResult, error) {
		if prompt != "worker fail prompt" {
			t.Fatalf("worker fail generator prompt = %q", prompt)
		}
		if len(refs) != 1 || string(refs[0]) != "fail-input-image" {
			t.Fatalf("worker fail generator refs = %#v", refs)
		}
		return nil, errors.New("fake upstream image failure")
	}
	_, created, err = store.CreateTask(ctx, DBImageTask{
		ID:             workerFailTaskID,
		ClientTaskID:   workerFailClientTaskID,
		OwnerID:        owner.ID,
		OwnerRole:      owner.Role,
		Status:         dbTaskStatusQueued,
		Mode:           "edit",
		Prompt:         "worker fail prompt",
		Model:          "gpt-image-2",
		ResponseFormat: "b64_json",
		N:              1,
		InputPaths:     failInputPaths,
	})
	if err != nil {
		t.Fatalf("create worker fail task: %v", err)
	}
	if !created {
		t.Fatalf("worker fail task was not created")
	}
	workerFailID := "worker_fail_" + suffix
	workerFailClaimed, ok, err := store.ClaimTaskByID(ctx, workerFailTaskID, workerFailID, time.Minute)
	if err != nil {
		t.Fatalf("claim worker fail task: %v", err)
	}
	if !ok {
		t.Fatalf("worker fail task was not claimed")
	}
	if err := workerServer.runDBImageTask(ctx, workerFailID, time.Minute, workerFailClaimed); err == nil || !strings.Contains(err.Error(), "fake upstream image failure") {
		t.Fatalf("run worker fail task err = %v, want fake upstream image failure", err)
	}
	workerFailed, err := store.GetTask(ctx, workerFailTaskID, owner)
	if err != nil {
		t.Fatalf("get worker failed task: %v", err)
	}
	if workerFailed.Status != dbTaskStatusError || !strings.Contains(workerFailed.Error, "fake upstream image failure") {
		t.Fatalf("worker failed task = %#v", workerFailed)
	}
	for _, path := range failInputPaths {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("worker fail input path should be cleaned: %s stat=%v", path, err)
		}
	}
	keys := workerServer.store.LoadAuthKeys()
	if len(keys) != 1 || keys[0].ImageDailyUsed != 0 || keys[0].ImageMonthlyUsed != 0 || keys[0].ImageTotalUsed != 0 {
		t.Fatalf("worker failure should refund image quota, keys=%#v", keys)
	}

	_, created, err = store.CreateTask(ctx, DBImageTask{
		ID:           cancelTaskID,
		ClientTaskID: cancelClientTaskID,
		OwnerID:      owner.ID,
		OwnerRole:    owner.Role,
		Status:       dbTaskStatusQueued,
		Mode:         "generate",
		Prompt:       "integration cancel prompt",
		Model:        "gpt-image-2",
		N:            1,
	})
	if err != nil {
		t.Fatalf("create cancel task: %v", err)
	}
	if !created {
		t.Fatalf("cancel task was not created")
	}
	canceled, err := store.CancelTasksByClientTaskID(ctx, cancelClientTaskID, owner)
	if err != nil {
		t.Fatalf("cancel by client_task_id: %v", err)
	}
	if len(canceled) != 1 || canceled[0].ID != cancelTaskID || canceled[0].Status != dbTaskStatusCanceled {
		t.Fatalf("canceled tasks = %#v", canceled)
	}
	if _, err := store.CancelTasksByClientTaskID(ctx, cancelClientTaskID, admin); !errors.Is(err, errAdminClientTaskOwnerRequired) {
		t.Fatalf("admin direct client_task_id cancel err = %v, want owner-required", err)
	}
}

func TestHTTPPGAsyncImageTaskLifecycle(t *testing.T) {
	databaseURL := strings.TrimSpace(os.Getenv("GPT2API_IMAGE_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("set GPT2API_IMAGE_TEST_DATABASE_URL to run PostgreSQL HTTP async lifecycle test")
	}
	t.Setenv("GPT2API_IMAGE_AUTH_KEY", "")
	t.Setenv("GPT2API_IMAGE_DATABASE_URL", "")
	t.Setenv("GPT2API_IMAGE_BASE_URL", "")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	suffix := randID(10)
	root := t.TempDir()
	rawConfig, _ := json.Marshal(map[string]any{
		"auth-key":                        "admin-" + suffix,
		"database_url":                    databaseURL,
		"base_url":                        "https://example.com",
		"image_retention_days":            15,
		"image_poll_timeout_secs":         1,
		"image_task_timeout_secs":         60,
		"image_task_claim_ttl_secs":       60,
		"image_worker_poll_interval_secs": 1,
		"image_account_concurrency":       1,
	})
	if err := os.WriteFile(filepath.Join(root, "config.json"), rawConfig, 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	s, err := newServer(root, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	defer s.taskStore.Close()

	userKey := "user-key-" + suffix
	ownerID := "owner-http-" + suffix
	if err := s.store.SaveAuthKeys([]UserKey{{
		ID:                    ownerID,
		Name:                  "http owner",
		Role:                  "user",
		KeyHash:               hashKey(userKey),
		Enabled:               true,
		ImageDailyUnlimited:   true,
		ImageMonthlyUnlimited: true,
		ImageTotalUnlimited:   true,
	}}); err != nil {
		t.Fatalf("save auth key: %v", err)
	}
	defer func() {
		_, _ = s.taskStore.db.ExecContext(context.Background(), `DELETE FROM image_tasks_v3 WHERE owner_id=$1`, ownerID)
	}()

	doJSON := func(method, path, token, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		rr := httptest.NewRecorder()
		s.Handler().ServeHTTP(rr, req)
		return rr
	}

	clientTaskID := "http-client-" + suffix
	createBody, _ := json.Marshal(map[string]any{
		"client_task_id":  clientTaskID,
		"model":           "gpt-image-2",
		"prompt":          "http async prompt",
		"n":               1,
		"size":            "1:1",
		"resolution":      "1k",
		"response_format": "b64_json",
	})
	rr := doJSON(http.MethodPost, "/api/image-tasks/generations", userKey, string(createBody))
	if rr.Code != http.StatusOK {
		t.Fatalf("create task status=%d body=%s", rr.Code, rr.Body.String())
	}
	var created ImageTask
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created task: %v", err)
	}
	if created.ID == "" || created.ClientTaskID != clientTaskID || created.Status != dbTaskStatusQueued {
		t.Fatalf("created task = %#v", created)
	}

	rr = doJSON(http.MethodGet, "/api/image-tasks?ids="+clientTaskID, userKey, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("query queued task status=%d body=%s", rr.Code, rr.Body.String())
	}
	var listed struct {
		Items      []ImageTask `json:"items"`
		MissingIDs []string    `json:"missing_ids"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode queued list: %v", err)
	}
	if len(listed.Items) != 1 || len(listed.MissingIDs) != 0 || listed.Items[0].ID != created.ID {
		t.Fatalf("queued list = %#v", listed)
	}

	s.imageGenerator = func(ctx context.Context, prompt, model, size, resolution string, refs [][]byte, n int) ([]upstreamImageResult, error) {
		if prompt != "http async prompt" || model != "gpt-image-2" || size != "1:1" || resolution != "1k" || n != 1 {
			t.Fatalf("generator args prompt=%q model=%q size=%q resolution=%q n=%d", prompt, model, size, resolution, n)
		}
		if len(refs) != 0 {
			t.Fatalf("generator refs = %#v, want empty", refs)
		}
		return []upstreamImageResult{{Bytes: []byte("http-generated-image"), RevisedPrompt: "http revised prompt"}}, nil
	}
	workerID := "http-worker-" + suffix
	claimed, ok, err := s.taskStore.ClaimTaskByID(ctx, created.ID, workerID, time.Minute)
	if err != nil {
		t.Fatalf("claim HTTP task: %v", err)
	}
	if !ok {
		t.Fatalf("HTTP task was not claimed")
	}
	if err := s.runDBImageTask(ctx, workerID, time.Minute, claimed); err != nil {
		t.Fatalf("run HTTP task: %v", err)
	}

	rr = doJSON(http.MethodGet, "/api/image-tasks?ids="+clientTaskID, userKey, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("query completed task status=%d body=%s", rr.Code, rr.Body.String())
	}
	listed = struct {
		Items      []ImageTask `json:"items"`
		MissingIDs []string    `json:"missing_ids"`
	}{}
	if err := json.Unmarshal(rr.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode completed list: %v", err)
	}
	if len(listed.Items) != 1 || listed.Items[0].Status != dbTaskStatusSuccess || len(listed.Items[0].Data) != 1 {
		t.Fatalf("completed list = %#v", listed)
	}
	if got := strings.TrimSpace(strAny(listed.Items[0].Data[0]["b64_json"], "")); got == "" {
		t.Fatalf("completed task missing b64_json: %#v", listed.Items[0].Data[0])
	}
	if got := strings.TrimSpace(strAny(listed.Items[0].Data[0]["url"], "")); !strings.HasPrefix(got, "https://example.com/images/") {
		t.Fatalf("completed task url = %q", got)
	}

	cancelClientTaskID := "http-cancel-" + suffix
	cancelCreateBody, _ := json.Marshal(map[string]any{
		"client_task_id": cancelClientTaskID,
		"model":          "gpt-image-2",
		"prompt":         "http cancel prompt",
		"n":              1,
	})
	rr = doJSON(http.MethodPost, "/api/image-tasks/generations", userKey, string(cancelCreateBody))
	if rr.Code != http.StatusOK {
		t.Fatalf("create cancel task status=%d body=%s", rr.Code, rr.Body.String())
	}
	cancelBody, _ := json.Marshal(map[string]any{"ids": []string{cancelClientTaskID}})
	rr = doJSON(http.MethodPost, "/api/image-tasks/cancel", userKey, string(cancelBody))
	if rr.Code != http.StatusOK {
		t.Fatalf("cancel task status=%d body=%s", rr.Code, rr.Body.String())
	}
	var cancelResult struct {
		Canceled []string `json:"canceled"`
		Missing  []string `json:"missing_ids"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &cancelResult); err != nil {
		t.Fatalf("decode cancel result: %v", err)
	}
	if len(cancelResult.Canceled) != 1 || len(cancelResult.Missing) != 0 {
		t.Fatalf("cancel result = %#v", cancelResult)
	}
}
