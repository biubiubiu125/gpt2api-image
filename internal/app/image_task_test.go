package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNormalizeImageResponseFormat(t *testing.T) {
	if got := normalizeImageResponseFormat(""); got != "b64_json" {
		t.Fatalf("empty response_format = %q, want b64_json", got)
	}
	if got := normalizeImageResponseFormat("url"); got != "url" {
		t.Fatalf("url response_format = %q, want url", got)
	}
	if got := normalizeImageResponseFormat("b64_json"); got != "b64_json" {
		t.Fatalf("b64_json response_format = %q, want b64_json", got)
	}
}

func TestImageTaskIDNamespacedByOwner(t *testing.T) {
	clientID := "client-task-1"
	a := imageTaskID("user-a", clientID)
	b := imageTaskID("user-b", clientID)
	if a == b {
		t.Fatalf("task ids should be owner-scoped: %q", a)
	}
	if imageTaskID("user-a", clientID) != a {
		t.Fatalf("task id should be stable for the same owner/client id")
	}
}

func TestPossibleImageTaskIDsIncludesClientAndDerivedID(t *testing.T) {
	ids := possibleImageTaskIDs("user-a", "client-task-1")
	if len(ids) != 2 {
		t.Fatalf("possible ids len = %d, want 2", len(ids))
	}
	if ids[0] != "client-task-1" {
		t.Fatalf("first id = %q, want original client id", ids[0])
	}
	if ids[1] != imageTaskID("user-a", "client-task-1") {
		t.Fatalf("derived id = %q, want %q", ids[1], imageTaskID("user-a", "client-task-1"))
	}
}

func TestDBImageTaskPublicIncludesStoredTaskMetadata(t *testing.T) {
	task := DBImageTask{
		ID:             "task-1",
		ClientTaskID:   "client-1",
		OwnerID:        "owner-1",
		OwnerRole:      "admin",
		Status:         dbTaskStatusQueued,
		Mode:           "generate",
		Model:          "gpt-image-2",
		Size:           "1:1",
		Resolution:     "1k",
		ResponseFormat: "b64_json",
		N:              3,
		CreatedAt:      nowISO(),
		UpdatedAt:      nowISO(),
	}

	got := task.Public()
	if got.OwnerRole != task.OwnerRole || got.N != task.N || got.ResponseFormat != task.ResponseFormat {
		t.Fatalf("public task metadata = %#v, want owner_role=%q n=%d response_format=%q", got, task.OwnerRole, task.N, task.ResponseFormat)
	}
}

func TestLocalImageTaskCreateWithDuplicateClientTaskIDReturnsExistingTask(t *testing.T) {
	store := NewStore(t.TempDir())
	clientTaskID := "client-task-1"
	existing := ImageTask{
		ID:           imageTaskID("admin", clientTaskID),
		ClientTaskID: clientTaskID,
		OwnerID:      "admin",
		OwnerRole:    "admin",
		Status:       "success",
		Mode:         "generate",
		Model:        "gpt-image-2",
		N:            1,
		CreatedAt:    nowISO(),
		UpdatedAt:    nowISO(),
		Data:         []map[string]any{{"url": "https://example.com/existing.png"}},
	}
	if err := store.SaveTasks([]ImageTask{existing}); err != nil {
		t.Fatalf("save existing task: %v", err)
	}
	s := &Server{cfg: Config{AuthKey: "root-key"}, store: store}
	req := httptest.NewRequest(http.MethodPost, "/api/image-tasks/generations", strings.NewReader(`{"client_task_id":"client-task-1","prompt":"new prompt"}`))
	req.Header.Set("Authorization", "Bearer root-key")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	s.handleImageTaskGeneration(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", rr.Code, rr.Body.String())
	}
	var got ImageTask
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode task: %v", err)
	}
	if got.ID != existing.ID || got.Status != existing.Status || len(got.Data) != 1 {
		t.Fatalf("returned task = %#v, want existing task", got)
	}
	tasks := store.LoadTasks()
	if len(tasks) != 1 || tasks[0].Status != existing.Status || len(tasks[0].Data) != 1 {
		t.Fatalf("stored tasks = %#v, want unchanged existing task", tasks)
	}
}

func TestImageTaskEndpointsRejectWrongMethods(t *testing.T) {
	s := &Server{cfg: Config{AuthKey: "root-key"}, store: NewStore(t.TempDir())}
	cases := []struct {
		name   string
		method string
		target string
		call   func(http.ResponseWriter, *http.Request)
	}{
		{name: "list", method: http.MethodPost, target: "/api/image-tasks", call: s.handleImageTasks},
		{name: "generate", method: http.MethodGet, target: "/api/image-tasks/generations", call: s.handleImageTaskGeneration},
		{name: "edit", method: http.MethodGet, target: "/api/image-tasks/edits", call: s.handleImageTaskEdit},
		{name: "cancel", method: http.MethodGet, target: "/api/image-tasks/cancel", call: s.handleImageTaskCancel},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.target, nil)
			req.Header.Set("Authorization", "Bearer root-key")
			rr := httptest.NewRecorder()

			tc.call(rr, req)

			if rr.Code != http.StatusMethodNotAllowed {
				t.Fatalf("status = %d body=%s, want 405", rr.Code, rr.Body.String())
			}
		})
	}
}

func TestV1EndpointsRejectWrongMethods(t *testing.T) {
	s := &Server{cfg: Config{AuthKey: "root-key"}, store: NewStore(t.TempDir())}
	cases := []struct {
		name   string
		method string
		target string
		allow  string
		call   func(http.ResponseWriter, *http.Request)
	}{
		{name: "models", method: http.MethodPost, target: "/v1/models", allow: http.MethodGet, call: s.handleV1Models},
		{name: "generations", method: http.MethodGet, target: "/v1/images/generations", allow: http.MethodPost, call: s.handleV1ImagesGenerations},
		{name: "edits", method: http.MethodGet, target: "/v1/images/edits", allow: http.MethodPost, call: s.handleV1ImagesEdits},
		{name: "chat completions", method: http.MethodGet, target: "/v1/chat/completions", allow: http.MethodPost, call: s.handleV1ChatCompletionsImageOnly},
		{name: "responses", method: http.MethodGet, target: "/v1/responses", allow: http.MethodPost, call: s.handleV1ResponsesImageOnly},
		{name: "messages disabled", method: http.MethodGet, target: "/v1/messages", allow: http.MethodPost, call: s.handleV1MessagesDisabled},
		{name: "messages full", method: http.MethodGet, target: "/v1/messages", allow: http.MethodPost, call: s.handleV1Messages},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.target, nil)
			rr := httptest.NewRecorder()

			tc.call(rr, req)

			if rr.Code != http.StatusMethodNotAllowed {
				t.Fatalf("status = %d body=%s, want 405", rr.Code, rr.Body.String())
			}
			if got := rr.Header().Get("Allow"); got != tc.allow {
				t.Fatalf("Allow = %q, want %q", got, tc.allow)
			}
		})
	}
}

func TestImageManagementEndpointsRejectWrongMethods(t *testing.T) {
	s := &Server{cfg: Config{AuthKey: "root-key"}, store: NewStore(t.TempDir())}
	cases := []struct {
		name   string
		method string
		target string
		allow  string
		call   func(http.ResponseWriter, *http.Request)
	}{
		{name: "admin images", method: http.MethodPost, target: "/api/images", allow: http.MethodGet, call: s.handleImages},
		{name: "my images", method: http.MethodPost, target: "/api/me/images", allow: http.MethodGet, call: s.handleMyImages},
		{name: "image owners", method: http.MethodPost, target: "/api/images/owners", allow: http.MethodGet, call: s.handleImageOwners},
		{name: "delete images", method: http.MethodGet, target: "/api/images/delete", allow: http.MethodPost, call: s.handleImageDelete},
		{name: "download images", method: http.MethodGet, target: "/api/images/download", allow: http.MethodPost, call: s.handleImageDownload},
		{name: "download single image", method: http.MethodPost, target: "/api/images/download/2026/07/05/a.png", allow: "GET, HEAD", call: s.handleImageDownloadSingle},
		{name: "thumbnail", method: http.MethodPost, target: "/image-thumbnails/2026/07/05/a.png", allow: "GET, HEAD", call: s.handleThumbnail},
		{name: "image tags", method: http.MethodPut, target: "/api/images/tags", allow: "GET, POST", call: s.handleImageTags},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.target, nil)
			rr := httptest.NewRecorder()

			tc.call(rr, req)

			if rr.Code != http.StatusMethodNotAllowed {
				t.Fatalf("status = %d body=%s, want 405", rr.Code, rr.Body.String())
			}
			if got := rr.Header().Get("Allow"); got != tc.allow {
				t.Fatalf("Allow = %q, want %q", got, tc.allow)
			}
		})
	}
}

func TestUpdateTaskStatusDoesNotOverrideCanceledTask(t *testing.T) {
	store := NewStore(t.TempDir())
	if err := store.SaveTasks([]ImageTask{{
		ID:        "task-1",
		OwnerID:   "user-a",
		Status:    "canceled",
		Mode:      "generate",
		CreatedAt: nowISO(),
		UpdatedAt: nowISO(),
	}}); err != nil {
		t.Fatalf("save tasks: %v", err)
	}
	s := &Server{store: store}
	updated, err := s.updateTaskStatus("task-1", "success", "", []map[string]any{{"url": "https://example.com/image.png"}})
	if err != nil {
		t.Fatalf("update canceled task: %v", err)
	}
	if updated {
		t.Fatal("canceled task should not be updated to success")
	}
	got := store.LoadTasks()[0]
	if got.Status != "canceled" || len(got.Data) != 0 {
		t.Fatalf("task after update = %#v, want canceled without data", got)
	}

	if err := store.SaveTasks([]ImageTask{{
		ID:        "task-2",
		OwnerID:   "user-a",
		Status:    "running",
		Mode:      "generate",
		CreatedAt: nowISO(),
		UpdatedAt: nowISO(),
	}}); err != nil {
		t.Fatalf("save running task: %v", err)
	}
	updated, err = s.updateTaskStatus("task-2", "success", "", []map[string]any{{"url": "https://example.com/image.png"}})
	if err != nil {
		t.Fatalf("update running task: %v", err)
	}
	if !updated {
		t.Fatal("running task should be updated")
	}
	got = store.LoadTasks()[0]
	if got.Status != "success" || len(got.Data) != 1 {
		t.Fatalf("task after running update = %#v, want success with data", got)
	}
}

func TestSaveTaskReturnsStoreError(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	if err := os.Mkdir(filepath.Join(dir, "image_tasks.json.tmp"), 0755); err != nil {
		t.Fatalf("block task writes: %v", err)
	}
	s := &Server{store: store}

	err := s.saveTask(ImageTask{
		ID:        "task-1",
		OwnerID:   "user-a",
		Status:    "running",
		Mode:      "generate",
		CreatedAt: nowISO(),
		UpdatedAt: nowISO(),
	})
	if err == nil {
		t.Fatal("saveTask error = nil, want store write error")
	}
	if got := store.LoadTasks(); len(got) != 0 {
		t.Fatalf("tasks after failed save = %#v, want empty", got)
	}
}

func TestUpdateTaskStatusReturnsStoreError(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	if err := store.SaveTasks([]ImageTask{{
		ID:        "task-1",
		OwnerID:   "user-a",
		Status:    "running",
		Mode:      "generate",
		CreatedAt: nowISO(),
		UpdatedAt: nowISO(),
	}}); err != nil {
		t.Fatalf("save tasks: %v", err)
	}
	if err := os.Mkdir(filepath.Join(dir, "image_tasks.json.tmp"), 0755); err != nil {
		t.Fatalf("block task writes: %v", err)
	}
	s := &Server{store: store}

	updated, err := s.updateTaskStatus("task-1", "success", "", []map[string]any{{"url": "https://example.com/image.png"}})
	if err == nil {
		t.Fatal("updateTaskStatus error = nil, want store write error")
	}
	if !updated {
		t.Fatal("updateTaskStatus updated = false, want true before write failure")
	}
	got := store.LoadTasks()[0]
	if got.Status != "running" || len(got.Data) != 0 {
		t.Fatalf("task after failed update = %#v, want original running task", got)
	}
}

func TestNonDBImageTaskCancelDoesNotCancelWhenStoreUpdateFails(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	taskID := "task-1"
	if err := store.SaveTasks([]ImageTask{{
		ID:        taskID,
		OwnerID:   "user-a",
		OwnerRole: "user",
		Status:    "running",
		Mode:      "generate",
		N:         1,
		CreatedAt: nowISO(),
		UpdatedAt: nowISO(),
	}}); err != nil {
		t.Fatalf("save tasks: %v", err)
	}
	if err := os.Mkdir(filepath.Join(dir, "image_tasks.json.tmp"), 0755); err != nil {
		t.Fatalf("block task writes: %v", err)
	}
	canceled := false
	s := &Server{
		cfg:         Config{AuthKey: "root-key"},
		store:       store,
		taskCancels: map[string]context.CancelFunc{taskID: func() { canceled = true }},
	}
	req := httptest.NewRequest(http.MethodPost, "/api/image-tasks/cancel", strings.NewReader(`{"ids":["task-1"]}`))
	req.Header.Set("Authorization", "Bearer root-key")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	s.handleImageTaskCancel(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body=%s, want 500", rr.Code, rr.Body.String())
	}
	if canceled {
		t.Fatal("cancel callback was called before task status write succeeded")
	}
	if _, ok := s.taskCancels[taskID]; !ok {
		t.Fatal("cancel callback was removed before task status write succeeded")
	}
	got := store.LoadTasks()[0]
	if got.Status != "running" {
		t.Fatalf("task status = %q, want running", got.Status)
	}
}

func TestNonDBImageTasksLookupAcceptsOriginalClientTaskID(t *testing.T) {
	store := NewStore(t.TempDir())
	clientTaskID := "client-task-1"
	taskID := newImageTaskID("user-a", clientTaskID)
	if err := store.SaveTasks([]ImageTask{{
		ID:           taskID,
		ClientTaskID: clientTaskID,
		OwnerID:      "user-a",
		Status:       "running",
		Mode:         "generate",
		CreatedAt:    nowISO(),
		UpdatedAt:    nowISO(),
	}}); err != nil {
		t.Fatalf("save tasks: %v", err)
	}
	s := &Server{cfg: Config{AuthKey: "root-key"}, store: store}
	req := httptest.NewRequest(http.MethodGet, "/api/image-tasks?ids="+clientTaskID+"&owner_id=user-a", nil)
	req.Header.Set("Authorization", "Bearer root-key")
	rr := httptest.NewRecorder()

	s.handleImageTasks(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", rr.Code, rr.Body.String())
	}
	var body struct {
		Items      []ImageTask `json:"items"`
		MissingIDs []string    `json:"missing_ids"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Items) != 1 || body.Items[0].ID != taskID {
		t.Fatalf("items = %#v, want task %s", body.Items, taskID)
	}
	if len(body.MissingIDs) != 0 {
		t.Fatalf("missing_ids = %#v, want empty", body.MissingIDs)
	}
}

func TestNonDBImageTaskCancelSkipsOtherOwnerSameClientTaskID(t *testing.T) {
	store := NewStore(t.TempDir())
	clientTaskID := "client-task-1"
	userTaskID := newImageTaskID("user-a", clientTaskID)
	otherTaskID := newImageTaskID("user-b", clientTaskID)
	now := nowISO()
	if err := store.SaveTasks([]ImageTask{
		{
			ID:           otherTaskID,
			ClientTaskID: clientTaskID,
			OwnerID:      "user-b",
			OwnerRole:    "user",
			Status:       "running",
			Mode:         "generate",
			N:            1,
			CreatedAt:    now,
			UpdatedAt:    now,
		},
		{
			ID:           userTaskID,
			ClientTaskID: clientTaskID,
			OwnerID:      "user-a",
			OwnerRole:    "user",
			Status:       "running",
			Mode:         "generate",
			N:            1,
			CreatedAt:    now,
			UpdatedAt:    now,
		},
	}); err != nil {
		t.Fatalf("save tasks: %v", err)
	}
	userCanceled := false
	otherCanceled := false
	s := &Server{
		cfg:   Config{AuthKey: "root-key"},
		store: store,
		taskCancels: map[string]context.CancelFunc{
			userTaskID:  func() { userCanceled = true },
			otherTaskID: func() { otherCanceled = true },
		},
	}
	req := httptest.NewRequest(http.MethodPost, "/api/image-tasks/cancel", strings.NewReader(`{"ids":["client-task-1"],"owner_id":"user-a"}`))
	req.Header.Set("Authorization", "Bearer root-key")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	s.handleImageTaskCancel(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", rr.Code, rr.Body.String())
	}
	var body struct {
		Canceled   []string `json:"canceled"`
		Skipped    []string `json:"skipped"`
		MissingIDs []string `json:"missing_ids"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Canceled) != 1 || body.Canceled[0] != userTaskID {
		t.Fatalf("canceled = %#v, want only %s", body.Canceled, userTaskID)
	}
	if len(body.Skipped) != 0 || len(body.MissingIDs) != 0 {
		t.Fatalf("skipped=%#v missing=%#v, want empty", body.Skipped, body.MissingIDs)
	}
	if !userCanceled {
		t.Fatal("user task cancel callback was not called")
	}
	if otherCanceled {
		t.Fatal("other owner task cancel callback was called")
	}
	tasks := store.LoadTasks()
	statusByID := map[string]string{}
	for _, task := range tasks {
		statusByID[task.ID] = task.Status
	}
	if statusByID[userTaskID] != "canceled" {
		t.Fatalf("user task status = %q, want canceled", statusByID[userTaskID])
	}
	if statusByID[otherTaskID] != "running" {
		t.Fatalf("other task status = %q, want running", statusByID[otherTaskID])
	}
	if _, ok := s.taskCancels[otherTaskID]; !ok {
		t.Fatal("other owner cancel callback should remain registered")
	}
}

func TestImageTaskIDLengthBounded(t *testing.T) {
	clientID := strings.Repeat("client-task-", 40)
	id := imageTaskID("user-a", clientID)
	maxLen := len("usr_") + 12 + 1 + maxSafeFileNameLen
	if len(id) > maxLen {
		t.Fatalf("task id len = %d, want <= %d", len(id), maxLen)
	}
	if imageTaskID("user-a", clientID) != id {
		t.Fatalf("bounded task id should still be stable")
	}
}

func TestImageTaskIDStableForUnicodeClientID(t *testing.T) {
	clientID := "任务一号"
	first := imageTaskID("user-a", clientID)
	second := imageTaskID("user-a", clientID)
	if first != second {
		t.Fatalf("unicode client task id should be stable: %q != %q", first, second)
	}
	if !strings.Contains(first, "id_") {
		t.Fatalf("unicode client task id should use deterministic hash suffix: %q", first)
	}
	ids := possibleImageTaskIDs("user-a", clientID)
	if len(ids) != 2 || ids[1] != first {
		t.Fatalf("possible ids = %#v, want original plus stable derived %q", ids, first)
	}
}

func TestSaveTaskInputImagesUsesUniquePaths(t *testing.T) {
	dir := t.TempDir()
	s := &Server{dataDir: dir}
	first, err := s.saveTaskInputImages("same-task", [][]byte{[]byte("first")})
	if err != nil {
		t.Fatalf("save first inputs: %v", err)
	}
	second, err := s.saveTaskInputImages("same-task", [][]byte{[]byte("second")})
	if err != nil {
		t.Fatalf("save second inputs: %v", err)
	}
	if len(first) != 1 || len(second) != 1 {
		t.Fatalf("input path counts = %d/%d, want 1/1", len(first), len(second))
	}
	if first[0] == second[0] {
		t.Fatalf("duplicate task input paths should not collide: %q", first[0])
	}
	gotFirst, _ := os.ReadFile(first[0])
	gotSecond, _ := os.ReadFile(second[0])
	if string(gotFirst) != "first" || string(gotSecond) != "second" {
		t.Fatalf("input files were overwritten: first=%q second=%q", gotFirst, gotSecond)
	}
}

func TestCleanupTaskInputPathsStaysUnderTaskInputs(t *testing.T) {
	root := t.TempDir()
	s := &Server{dataDir: filepath.Join(root, "data")}
	base := filepath.Join(s.dataDir, "task_inputs")
	insideDir := filepath.Join(s.dataDir, "task_inputs", "task-1")
	if err := os.MkdirAll(insideDir, 0755); err != nil {
		t.Fatalf("make task input dir: %v", err)
	}
	inside := filepath.Join(insideDir, "image.bin")
	outside := filepath.Join(root, "outside.bin")
	if err := os.WriteFile(inside, []byte("inside"), 0644); err != nil {
		t.Fatalf("write inside input: %v", err)
	}
	if err := os.WriteFile(outside, []byte("outside"), 0644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}

	s.cleanupTaskInputPaths([]string{inside, outside, base})

	if _, err := os.Stat(inside); !os.IsNotExist(err) {
		t.Fatalf("inside task input should be removed, stat err=%v", err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("outside file should not be touched: %v", err)
	}
	if _, err := os.Stat(base); err != nil {
		t.Fatalf("task input root should not be touched: %v", err)
	}
}

func TestImageTagWriteFailureReturnsServerError(t *testing.T) {
	newServerWithBlockedTags := func(t *testing.T) *Server {
		t.Helper()
		dir := t.TempDir()
		store := NewStore(dir)
		if err := os.Mkdir(filepath.Join(dir, "image_tags.json.tmp"), 0755); err != nil {
			t.Fatalf("block tag metadata writes: %v", err)
		}
		return &Server{cfg: Config{AuthKey: "root-key"}, store: store}
	}

	t.Run("post", func(t *testing.T) {
		s := newServerWithBlockedTags(t)
		req := httptest.NewRequest(http.MethodPost, "/api/images/tags", strings.NewReader(`{"path":"2026/07/05/a.png","tags":["x"]}`))
		req.Header.Set("Authorization", "Bearer root-key")
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()

		s.handleImageTags(rr, req)

		if rr.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d body=%s, want 500", rr.Code, rr.Body.String())
		}
		if len(s.store.LoadTags()) != 0 {
			t.Fatalf("tags = %#v, want empty after failed write", s.store.LoadTags())
		}
	})

	t.Run("delete", func(t *testing.T) {
		dir := t.TempDir()
		store := NewStore(dir)
		if err := store.SaveTags(map[string][]string{"2026/07/05/a.png": []string{"x", "y"}}); err != nil {
			t.Fatalf("save tags: %v", err)
		}
		if err := os.Mkdir(filepath.Join(dir, "image_tags.json.tmp"), 0755); err != nil {
			t.Fatalf("block tag metadata writes: %v", err)
		}
		s := &Server{cfg: Config{AuthKey: "root-key"}, store: store}
		req := httptest.NewRequest(http.MethodDelete, "/api/images/tags/x", nil)
		req.Header.Set("Authorization", "Bearer root-key")
		rr := httptest.NewRecorder()

		s.handleImageTagDelete(rr, req)

		if rr.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d body=%s, want 500", rr.Code, rr.Body.String())
		}
		got := s.store.LoadTags()["2026/07/05/a.png"]
		if strings.Join(got, ",") != "x,y" {
			t.Fatalf("tags = %#v, want original tags after failed delete", got)
		}
	})
}

func TestImageTagsRejectInvalidPath(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
	}{
		{name: "empty", path: ""},
		{name: "traversal", path: "../outside.png"},
		{name: "trimmed traversal", path: "/../outside.png"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := NewStore(t.TempDir())
			s := &Server{cfg: Config{AuthKey: "root-key"}, store: store}
			body := `{"path":"` + tc.path + `","tags":["x"]}`
			req := httptest.NewRequest(http.MethodPost, "/api/images/tags", strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer root-key")
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()

			s.handleImageTags(rr, req)

			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status = %d body=%s, want 400", rr.Code, rr.Body.String())
			}
			if got := store.LoadTags(); len(got) != 0 {
				t.Fatalf("tags = %#v, want empty", got)
			}
		})
	}
}

func TestImageTagDeleteRequiresDeleteMethod(t *testing.T) {
	store := NewStore(t.TempDir())
	if err := store.SaveTags(map[string][]string{"2026/07/05/a.png": []string{"x", "y"}}); err != nil {
		t.Fatalf("save tags: %v", err)
	}
	s := &Server{cfg: Config{AuthKey: "root-key"}, store: store}
	req := httptest.NewRequest(http.MethodGet, "/api/images/tags/x", nil)
	req.Header.Set("Authorization", "Bearer root-key")
	rr := httptest.NewRecorder()

	s.handleImageTagDelete(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d body=%s, want 405", rr.Code, rr.Body.String())
	}
	got := store.LoadTags()["2026/07/05/a.png"]
	if strings.Join(got, ",") != "x,y" {
		t.Fatalf("tags = %#v, want unchanged tags", got)
	}
}

func TestImageManagementFiltersOwnersAndBulkDelete(t *testing.T) {
	t.Setenv("GPT2API_IMAGE_AUTH_KEY", "")
	t.Setenv("GPT2API_IMAGE_DATABASE_URL", "")
	t.Setenv("GPT2API_IMAGE_BASE_URL", "")
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "config.json"), []byte(`{"auth-key":"root-key","image_retention_days":15}`), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	s, err := newServer(root, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	if err := s.store.SaveAuthKeys([]UserKey{{ID: "user-a", Name: "User A", Enabled: true}}); err != nil {
		t.Fatalf("save auth keys: %v", err)
	}

	writeImage := func(rel string, mod time.Time) {
		path := filepath.Join(s.imagesDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatalf("make image dir: %v", err)
		}
		if err := os.WriteFile(path, []byte("image"), 0644); err != nil {
			t.Fatalf("write image %s: %v", rel, err)
		}
		if err := os.Chtimes(path, mod, mod); err != nil {
			t.Fatalf("set image time %s: %v", rel, err)
		}
	}
	adminRel := "2026/07/01/admin.png"
	userRel := "2026/07/02/user.png"
	unownedRel := "2026/07/03/unowned.png"
	writeImage(adminRel, time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC))
	writeImage(userRel, time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC))
	writeImage(unownedRel, time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC))
	if err := s.store.UpdateOwners(func(owners map[string]string) map[string]string {
		owners[adminRel] = "admin"
		owners[userRel] = "user-a"
		return owners
	}); err != nil {
		t.Fatalf("save owners: %v", err)
	}
	if err := s.store.UpdatePrompts(func(prompts map[string]map[string]any) map[string]map[string]any {
		prompts[userRel] = map[string]any{"prompt": "user prompt"}
		return prompts
	}); err != nil {
		t.Fatalf("save prompts: %v", err)
	}
	if err := s.store.UpdateTags(func(tags map[string][]string) map[string][]string {
		tags[userRel] = []string{"tag-a"}
		return tags
	}); err != nil {
		t.Fatalf("save tags: %v", err)
	}

	do := func(method, target, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, target, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer root-key")
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		rr := httptest.NewRecorder()
		s.Handler().ServeHTTP(rr, req)
		return rr
	}

	rr := do(http.MethodGet, "/api/images?start_date=2026-07-02&end_date=2026-07-02&owner=user-a", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("list filtered images status = %d body=%s, want 200", rr.Code, rr.Body.String())
	}
	var listed struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&listed); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if len(listed.Items) != 1 || listed.Items[0]["rel"] != userRel {
		t.Fatalf("filtered items = %#v, want only %s", listed.Items, userRel)
	}

	rr = do(http.MethodGet, "/api/images/owners", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("owners status = %d body=%s, want 200", rr.Code, rr.Body.String())
	}
	var ownersResp struct {
		Items []struct {
			ID    string `json:"id"`
			Count int    `json:"count"`
		} `json:"items"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&ownersResp); err != nil {
		t.Fatalf("decode owners response: %v", err)
	}
	counts := map[string]int{}
	for _, item := range ownersResp.Items {
		counts[item.ID] = item.Count
	}
	if counts["__admin__"] != 1 || counts["user-a"] != 1 || counts["__unowned__"] != 1 {
		t.Fatalf("owner counts = %#v, want admin/user/unowned = 1", counts)
	}

	rr = do(http.MethodPost, "/api/images/delete", `{"start_date":"2026-07-02","end_date":"2026-07-02","owner":"user-a","all_matching":true}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("bulk delete status = %d body=%s, want 200", rr.Code, rr.Body.String())
	}
	var deleted struct {
		Removed int `json:"removed"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&deleted); err != nil {
		t.Fatalf("decode delete response: %v", err)
	}
	if deleted.Removed != 1 {
		t.Fatalf("removed = %d, want 1", deleted.Removed)
	}
	if _, err := os.Stat(filepath.Join(s.imagesDir, filepath.FromSlash(userRel))); !os.IsNotExist(err) {
		t.Fatalf("matching user image should be deleted, stat err=%v", err)
	}
	for _, rel := range []string{adminRel, unownedRel} {
		if _, err := os.Stat(filepath.Join(s.imagesDir, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("non-matching image %s should remain: %v", rel, err)
		}
	}
	if _, ok := s.store.LoadOwners()[userRel]; ok {
		t.Fatalf("deleted image owner metadata should be removed")
	}
	if _, ok := s.store.LoadPrompts()[userRel]; ok {
		t.Fatalf("deleted image prompt metadata should be removed")
	}
	if _, ok := s.store.LoadTags()[userRel]; ok {
		t.Fatalf("deleted image tag metadata should be removed")
	}
}

func TestStoredImagesDoNotExposeDirectoryListing(t *testing.T) {
	t.Setenv("GPT2API_IMAGE_AUTH_KEY", "")
	t.Setenv("GPT2API_IMAGE_DATABASE_URL", "")
	t.Setenv("GPT2API_IMAGE_BASE_URL", "")
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "config.json"), []byte(`{"auth-key":"root-key","image_retention_days":15}`), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	s, err := newServer(root, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	rel := "2026/07/05/image.png"
	path := filepath.Join(s.imagesDir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("make image dir: %v", err)
	}
	if err := os.WriteFile(path, []byte("image-data"), 0644); err != nil {
		t.Fatalf("write image: %v", err)
	}

	for _, target := range []string{"/images/", "/images/2026/", "/images/2026/07/05/"} {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		rr := httptest.NewRecorder()
		s.Handler().ServeHTTP(rr, req)
		if rr.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d body=%q, want 404", target, rr.Code, rr.Body.String())
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/images/"+rel, nil)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK || rr.Body.String() != "image-data" {
		t.Fatalf("stored image response status=%d body=%q, want image data", rr.Code, rr.Body.String())
	}
}

func TestImageBulkDeleteHonorsTagFilter(t *testing.T) {
	t.Setenv("GPT2API_IMAGE_AUTH_KEY", "")
	t.Setenv("GPT2API_IMAGE_DATABASE_URL", "")
	t.Setenv("GPT2API_IMAGE_BASE_URL", "")
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "config.json"), []byte(`{"auth-key":"root-key","image_retention_days":15}`), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	s, err := newServer(root, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	writeImage := func(rel string) {
		path := filepath.Join(s.imagesDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatalf("make image dir: %v", err)
		}
		if err := os.WriteFile(path, []byte("image"), 0644); err != nil {
			t.Fatalf("write image %s: %v", rel, err)
		}
	}
	taggedRel := "2026/07/05/tagged.png"
	otherRel := "2026/07/05/other.png"
	writeImage(taggedRel)
	writeImage(otherRel)
	if err := s.store.UpdateTags(func(tags map[string][]string) map[string][]string {
		tags[taggedRel] = []string{"delete-me"}
		tags[otherRel] = []string{"keep-me"}
		return tags
	}); err != nil {
		t.Fatalf("save tags: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/images/delete", strings.NewReader(`{"tags":["delete-me"],"all_matching":true}`))
	req.Header.Set("Authorization", "Bearer root-key")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("tag filtered delete status = %d body=%s, want 200", rr.Code, rr.Body.String())
	}
	var deleted struct {
		Removed int `json:"removed"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&deleted); err != nil {
		t.Fatalf("decode delete response: %v", err)
	}
	if deleted.Removed != 1 {
		t.Fatalf("removed = %d, want 1", deleted.Removed)
	}
	if _, err := os.Stat(filepath.Join(s.imagesDir, filepath.FromSlash(taggedRel))); !os.IsNotExist(err) {
		t.Fatalf("tagged image should be deleted, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(s.imagesDir, filepath.FromSlash(otherRel))); err != nil {
		t.Fatalf("non-matching tagged image should remain: %v", err)
	}
}

func TestAdminClientTaskFallbackRequiresOwner(t *testing.T) {
	store := &PGTaskStore{}
	admin := Identity{ID: "admin", Role: "admin"}
	if _, err := store.TasksByClientTaskID(context.Background(), "client-task-1", admin); !errors.Is(err, errAdminClientTaskOwnerRequired) {
		t.Fatalf("TasksByClientTaskID err = %v, want owner-required", err)
	}
	if _, err := store.CancelTasksByClientTaskID(context.Background(), "client-task-1", admin); !errors.Is(err, errAdminClientTaskOwnerRequired) {
		t.Fatalf("CancelTasksByClientTaskID err = %v, want owner-required", err)
	}
}

func TestScopedTaskIdentityUsesOwnerForAdmin(t *testing.T) {
	admin := &Identity{ID: "admin", Role: "admin"}
	got := scopedTaskIdentity(admin, "user-a")
	if got.ID != "user-a" || got.Role != "user" {
		t.Fatalf("scoped admin identity = %#v, want user-a/user", got)
	}
	user := &Identity{ID: "user-b", Role: "user"}
	got = scopedTaskIdentity(user, "user-a")
	if got.ID != "user-b" || got.Role != "user" {
		t.Fatalf("non-admin identity should not be overridden: %#v", got)
	}
}

func TestClientTaskIDFallbackScope(t *testing.T) {
	if canUseClientTaskIDFallback(Identity{ID: "admin", Role: "admin"}) {
		t.Fatalf("admin without owner scope should not use client_task_id fallback")
	}
	if !canUseClientTaskIDFallback(scopedTaskIdentity(&Identity{ID: "admin", Role: "admin"}, "user-a")) {
		t.Fatalf("admin scoped to owner should use client_task_id fallback")
	}
	if !canUseClientTaskIDFallback(Identity{ID: "user-a", Role: "user"}) {
		t.Fatalf("ordinary users should use client_task_id fallback")
	}
}

func TestEnqueueV1ImageTaskRequiresClientTaskID(t *testing.T) {
	s := &Server{taskStore: &PGTaskStore{}}
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{}`))
	rr := httptest.NewRecorder()
	ok := s.enqueueV1ImageTask(rr, req, &Identity{ID: "user-a", Role: "user"}, imageTaskCreateRequest{
		Mode:   "generate",
		Prompt: "sync prompt",
		Model:  "gpt-image-2",
		N:      1,
	})
	if ok {
		t.Fatalf("v1 image request without client_task_id should stay synchronous")
	}
}

func TestV1ChatAndResponsesWithoutClientTaskIDStaySynchronousWithDB(t *testing.T) {
	s, _ := newV1ImageCleanupTestServer(t)
	s.taskStore = &PGTaskStore{}
	generateCalls := 0
	s.imageGenerator = func(ctx context.Context, prompt, model, size, resolution string, refs [][]byte, n int) ([]upstreamImageResult, error) {
		generateCalls++
		return []upstreamImageResult{{Bytes: []byte("generated-image"), RevisedPrompt: prompt}}, nil
	}

	cases := []struct {
		name       string
		path       string
		body       string
		wantObject string
	}{
		{
			name:       "chat completions",
			path:       "/v1/chat/completions",
			body:       `{"model":"gpt-image-2","prompt":"draw a cat"}`,
			wantObject: "chat.completion",
		},
		{
			name:       "responses",
			path:       "/v1/responses",
			body:       `{"model":"gpt-image-2","input":"draw a cat","tools":[{"type":"image_generation"}]}`,
			wantObject: "response",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer test")
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()

			s.Handler().ServeHTTP(rr, req)

			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d body=%s, want 200", rr.Code, rr.Body.String())
			}
			var got map[string]any
			if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if got["object"] != tc.wantObject {
				t.Fatalf("object = %#v, want %q; body=%s", got["object"], tc.wantObject, rr.Body.String())
			}
			if _, ok := got["task"]; ok {
				t.Fatalf("response should be synchronous without client_task_id, got task body=%s", rr.Body.String())
			}
			if got["status"] == "queued" {
				t.Fatalf("response should not be queued without client_task_id, body=%s", rr.Body.String())
			}
		})
	}
	if generateCalls != len(cases) {
		t.Fatalf("image generation calls = %d, want %d", generateCalls, len(cases))
	}
}

func TestChatStreamImageWithoutClientTaskIDStaysSynchronousWithDB(t *testing.T) {
	s, _ := newV1ImageCleanupTestServer(t)
	s.taskStore = &PGTaskStore{}
	generateCalls := 0
	s.imageGenerator = func(ctx context.Context, prompt, model, size, resolution string, refs [][]byte, n int) ([]upstreamImageResult, error) {
		generateCalls++
		return []upstreamImageResult{{Bytes: []byte("generated-image"), RevisedPrompt: prompt}}, nil
	}

	req := httptest.NewRequest(http.MethodPost, "/api/chat/stream", strings.NewReader(`{"model":"gpt-image-2","prompt":"draw a cat"}`))
	req.Header.Set("Authorization", "Bearer test")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	s.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if strings.Contains(body, "image_task.queued") {
		t.Fatalf("body = %s, want synchronous image stream without queued task event", body)
	}
	if !strings.Contains(body, "conversation.delta") || !strings.Contains(body, "conversation.done") {
		t.Fatalf("body = %s, want synchronous conversation stream events", body)
	}
	if generateCalls != 1 {
		t.Fatalf("image generation calls = %d, want 1", generateCalls)
	}
}

func TestImageTaskEditRejectsEmptyPromptBeforeReadingInputs(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("prompt", "   "); err != nil {
		t.Fatalf("write prompt: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	s := &Server{cfg: Config{AuthKey: "root-key"}, store: NewStore(t.TempDir())}
	req := httptest.NewRequest(http.MethodPost, "/api/image-tasks/edits", &body)
	req.Header.Set("Authorization", "Bearer root-key")
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rr := httptest.NewRecorder()

	s.handleImageTaskEdit(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s, want 400", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "prompt is required") {
		t.Fatalf("body = %s, want prompt error", rr.Body.String())
	}
}

func TestFreeUserHighResolutionRejectedBeforeDBTaskCreate(t *testing.T) {
	s := &Server{taskStore: &PGTaskStore{}}
	_, _, err := s.createDBImageTask(context.Background(), &Identity{
		ID:                      "user-a",
		Role:                    "user",
		AccountTier:             "free",
		CanUsePaidImageAccounts: false,
		CanUseHighResolution:    false,
	}, imageTaskCreateRequest{
		Mode:       "generate",
		Prompt:     "high resolution prompt",
		Model:      "gpt-image-2",
		Resolution: "2k",
		N:          1,
	})
	var se statusError
	if !errors.As(err, &se) || se.status != http.StatusForbidden {
		t.Fatalf("create high resolution task err = %v, want 403 statusError", err)
	}
}

func TestStoredAuthKeyPreservesRoleAndQuota(t *testing.T) {
	s := &Server{
		cfg:   Config{AuthKey: "root-key"},
		store: NewStore(t.TempDir()),
	}
	if err := s.store.SaveAuthKeys([]UserKey{{
		ID:                    "newapi",
		Name:                  "newapi",
		Role:                  "user",
		Key:                   "sk-newapi",
		KeyHash:               hashKey("sk-newapi"),
		AccountTier:           "free",
		Enabled:               true,
		ImageDailyUnlimited:   true,
		ImageMonthlyUnlimited: true,
		ImageTotalQuota:       1,
		ImageTotalUnlimited:   false,
		QuotaConfigured:       true,
	}}); err != nil {
		t.Fatalf("save auth keys: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/accounts", nil)
	req.Header.Set("Authorization", "Bearer sk-newapi")
	rr := httptest.NewRecorder()
	id, ok := s.requireIdentity(rr, req)
	if !ok {
		t.Fatalf("service key should authenticate, status=%d body=%s", rr.Code, rr.Body.String())
	}
	if id.ID != "newapi" || id.Role != "user" || id.AccountTier != "free" {
		t.Fatalf("identity = %#v, want newapi user free", id)
	}
	if id.CanUseHighResolution || id.CanUsePaidImageAccounts {
		t.Fatalf("free user key should not have paid image access: %#v", id)
	}
	if !s.consumeImage(id, 1) {
		t.Fatal("first image quota consume failed")
	}
	if s.consumeImage(id, 1) {
		t.Fatal("second image quota consume should fail")
	}
	keys := s.store.LoadAuthKeys()
	if len(keys) != 1 || keys[0].Role != "user" || keys[0].AccountTier != "free" || keys[0].ImageTotalUsed != 1 {
		t.Fatalf("stored key = %#v, want user free with one used image quota", keys)
	}
}

func TestCreateAuthUserEndpointCreatesServiceKey(t *testing.T) {
	s := &Server{
		cfg:   Config{AuthKey: "root-key"},
		store: NewStore(t.TempDir()),
	}
	req := httptest.NewRequest(http.MethodPost, "/api/auth/users", strings.NewReader(`{"name":"newapi","key":"sk-service"}`))
	req.Header.Set("Authorization", "Bearer root-key")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	s.handleAuthUsers(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("create key status = %d body=%s, want 200", rr.Code, rr.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	req.Header.Set("Authorization", "Bearer sk-service")
	rr = httptest.NewRecorder()
	id, ok := s.requireAdmin(rr, req)
	if !ok {
		t.Fatalf("created service key should pass admin auth, status=%d body=%s", rr.Code, rr.Body.String())
	}
	if id.Role != "admin" || id.AccountTier != "premium" {
		t.Fatalf("created key identity = %#v, want admin premium", id)
	}
	if !s.consumeImage(id, 1) {
		t.Fatal("default service key should have unlimited image quota")
	}
}

func TestCreateAuthUserEndpointAppliesRoleAndQuota(t *testing.T) {
	s := &Server{
		cfg:   Config{AuthKey: "root-key"},
		store: NewStore(t.TempDir()),
	}
	req := httptest.NewRequest(http.MethodPost, "/api/auth/users", strings.NewReader(`{"name":"limited","key":"sk-limited","role":"user","account_tier":"free","image_total_quota":1,"image_total_unlimited":false}`))
	req.Header.Set("Authorization", "Bearer root-key")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	s.handleAuthUsers(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("create key status = %d body=%s, want 200", rr.Code, rr.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	req.Header.Set("Authorization", "Bearer sk-limited")
	rr = httptest.NewRecorder()
	id, ok := s.requireIdentity(rr, req)
	if !ok {
		t.Fatalf("created limited key should authenticate, status=%d body=%s", rr.Code, rr.Body.String())
	}
	if id.Role != "user" || id.AccountTier != "free" || id.CanUseHighResolution || id.CanUsePaidImageAccounts {
		t.Fatalf("created limited key identity = %#v, want user free without paid image access", id)
	}
	if !s.consumeImage(id, 1) {
		t.Fatal("first limited key image quota consume failed")
	}
	if s.consumeImage(id, 1) {
		t.Fatal("second limited key image quota consume should fail")
	}
	keys := s.store.LoadAuthKeys()
	if len(keys) != 1 || !keys[0].QuotaConfigured || !keys[0].ImageDailyUnlimited || !keys[0].ImageMonthlyUnlimited || keys[0].ImageTotalUnlimited || keys[0].ImageTotalUsed != 1 {
		t.Fatalf("created limited key quota = %#v, want daily/monthly unlimited and one used limited total quota", keys)
	}
}

func TestAdminServiceKeyCanManageButImageQuotaIsEnforced(t *testing.T) {
	s := &Server{
		cfg:   Config{AuthKey: "root-key"},
		store: NewStore(t.TempDir()),
	}
	if err := s.store.SaveAuthKeys([]UserKey{{
		ID:                    "service",
		Name:                  "service",
		Role:                  "admin",
		Key:                   "sk-service",
		KeyHash:               hashKey("sk-service"),
		AccountTier:           "premium",
		Enabled:               true,
		ImageDailyUnlimited:   true,
		ImageMonthlyUnlimited: true,
		ImageTotalQuota:       1,
		ImageTotalUnlimited:   false,
		QuotaConfigured:       true,
	}}); err != nil {
		t.Fatalf("save auth keys: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	req.Header.Set("Authorization", "Bearer sk-service")
	rr := httptest.NewRecorder()
	id, ok := s.requireAdmin(rr, req)
	if !ok {
		t.Fatalf("service key should pass admin auth, status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !s.consumeImage(id, 1) {
		t.Fatal("first image quota consume failed")
	}
	if s.consumeImage(id, 1) {
		t.Fatal("second image quota consume should fail")
	}
	if s.consumeImage(&Identity{ID: "admin", Role: "admin", Root: true}, 1000) != true {
		t.Fatal("root admin should stay unlimited")
	}
	keys := s.store.LoadAuthKeys()
	if len(keys) != 1 || keys[0].ImageTotalUsed != 1 || keys[0].ImageTotalUnlimited {
		t.Fatalf("stored key quota = %#v, want one used limited total quota", keys)
	}
}

func TestAdminIDServiceKeyDoesNotBecomeRoot(t *testing.T) {
	s := &Server{
		cfg:   Config{AuthKey: "root-key"},
		store: NewStore(t.TempDir()),
	}
	if err := s.store.SaveAuthKeys([]UserKey{{
		ID:                    "admin",
		Name:                  "manual admin id",
		Role:                  "admin",
		Key:                   "sk-admin-id",
		KeyHash:               hashKey("sk-admin-id"),
		AccountTier:           "premium",
		Enabled:               true,
		ImageDailyUnlimited:   true,
		ImageMonthlyUnlimited: true,
		ImageTotalQuota:       1,
		ImageTotalUnlimited:   false,
		QuotaConfigured:       true,
	}}); err != nil {
		t.Fatalf("save auth keys: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	req.Header.Set("Authorization", "Bearer sk-admin-id")
	rr := httptest.NewRecorder()
	id, ok := s.requireAdmin(rr, req)
	if !ok {
		t.Fatalf("service key should pass admin auth, status=%d body=%s", rr.Code, rr.Body.String())
	}
	if isRootIdentity(id) {
		t.Fatalf("service key identity should not be root: %#v", id)
	}
	if !s.consumeImage(id, 1) {
		t.Fatal("first image quota consume failed")
	}
	if s.consumeImage(id, 1) {
		t.Fatal("service key with admin id should still obey image quota")
	}
}

func TestRegisterInternalRequiresDedicatedKey(t *testing.T) {
	s := &Server{
		cfg:   Config{AuthKey: "root-key"},
		store: NewStore(t.TempDir()),
	}

	req := httptest.NewRequest(http.MethodGet, "/internal/register/accounts", nil)
	req.Header.Set("Authorization", "Bearer root-key")
	rr := httptest.NewRecorder()
	s.handleInternalRegisterAccounts(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d body=%s, want 401 when internal key is unset", rr.Code, rr.Body.String())
	}

	s.cfg.RegisterInternalKey = "internal-key"
	req = httptest.NewRequest(http.MethodGet, "/internal/register/accounts", nil)
	req.Header.Set("Authorization", "Bearer root-key")
	rr = httptest.NewRecorder()
	s.handleInternalRegisterAccounts(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d body=%s, want 401 for root key on internal register route", rr.Code, rr.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/internal/register/accounts", nil)
	req.Header.Set("X-Register-Internal-Key", "internal-key")
	rr = httptest.NewRecorder()
	s.handleInternalRegisterAccounts(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200 with dedicated internal key", rr.Code, rr.Body.String())
	}
}

func TestRegisterExecutorProxyRequiresDedicatedInternalKey(t *testing.T) {
	s := &Server{
		cfg: Config{
			AuthKey:             "root-key",
			RegisterExecutorURL: "http://127.0.0.1:1",
		},
		store: NewStore(t.TempDir()),
	}
	req := httptest.NewRequest(http.MethodGet, "/api/register", nil)
	req.Header.Set("Authorization", "Bearer root-key")
	rr := httptest.NewRecorder()

	s.handleRegister(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body=%s, want 500", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "register_internal_key is required") {
		t.Fatalf("body = %s, want missing internal key error", rr.Body.String())
	}
}

func TestAuthUserRegenerateRequiresPostMethod(t *testing.T) {
	s := &Server{cfg: Config{AuthKey: "root-key"}, store: NewStore(t.TempDir())}
	initial := UserKey{ID: "user-a", Name: "User A", Key: "sk-user-a", KeyHash: hashKey("sk-user-a"), Enabled: true}
	if err := s.store.SaveAuthKeys([]UserKey{initial}); err != nil {
		t.Fatalf("save auth keys: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/auth/users/user-a/regenerate", nil)
	req.Header.Set("Authorization", "Bearer root-key")
	rr := httptest.NewRecorder()

	s.handleAuthUserID(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d body=%s, want 405", rr.Code, rr.Body.String())
	}
	keys := s.store.LoadAuthKeys()
	if len(keys) != 1 || keys[0].KeyHash != initial.KeyHash || keys[0].Key != initial.Key {
		t.Fatalf("auth keys changed after GET regenerate: %#v", keys)
	}
}

func TestAuthUserEndpointWriteFailuresReturnServerError(t *testing.T) {
	cases := []struct {
		name   string
		method string
		target string
		body   string
		call   func(*Server, http.ResponseWriter, *http.Request)
	}{
		{
			name:   "create",
			method: http.MethodPost,
			target: "/api/auth/users",
			body:   `{"name":"newapi","key":"sk-service"}`,
			call:   (*Server).handleAuthUsers,
		},
		{
			name:   "regenerate",
			method: http.MethodPost,
			target: "/api/auth/users/user-a/regenerate",
			body:   `{"key":"sk-rotated"}`,
			call:   (*Server).handleAuthUserID,
		},
		{
			name:   "delete",
			method: http.MethodDelete,
			target: "/api/auth/users/user-a",
			call:   (*Server).handleAuthUserID,
		},
		{
			name:   "update",
			method: http.MethodPost,
			target: "/api/auth/users/user-a",
			body:   `{"name":"renamed"}`,
			call:   (*Server).handleAuthUserID,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{
				cfg:   Config{AuthKey: "root-key"},
				store: NewStore(t.TempDir()),
			}
			initial := UserKey{ID: "user-a", Name: "User A", Key: "sk-user-a", KeyHash: hashKey("sk-user-a"), Enabled: true}
			if err := s.store.SaveAuthKeys([]UserKey{initial}); err != nil {
				t.Fatalf("save auth keys: %v", err)
			}
			if err := os.Mkdir(s.store.path("auth_keys.json.tmp"), 0755); err != nil {
				t.Fatalf("block auth key temp write: %v", err)
			}

			req := httptest.NewRequest(tc.method, tc.target, strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer root-key")
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			tc.call(s, rr, req)

			if rr.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d body=%s, want 500", rr.Code, rr.Body.String())
			}
			keys := s.store.LoadAuthKeys()
			if len(keys) != 1 || keys[0].ID != initial.ID || keys[0].Name != initial.Name || keys[0].KeyHash != initial.KeyHash {
				t.Fatalf("auth keys changed after failed write: %#v", keys)
			}
		})
	}
}

func TestSaveImageWithBaseURLUniqueForSameBytes(t *testing.T) {
	root := t.TempDir()
	s := &Server{
		dataDir:   filepath.Join(root, "data"),
		imagesDir: filepath.Join(root, "data", "images"),
		store:     NewStore(filepath.Join(root, "data")),
		cfg:       Config{ImageRetentionDays: 15},
	}
	relA, _, err := s.saveImageWithBaseURL("https://example.com", []byte("same-image"))
	if err != nil {
		t.Fatalf("save first image: %v", err)
	}
	relB, _, err := s.saveImageWithBaseURL("https://example.com", []byte("same-image"))
	if err != nil {
		t.Fatalf("save second image: %v", err)
	}
	if relA == relB {
		t.Fatalf("same bytes saved twice should get unique rel path: %q", relA)
	}
}

func TestSaveImageWithBaseURLUsesDetectedImageExtension(t *testing.T) {
	root := t.TempDir()
	s := &Server{
		dataDir:   filepath.Join(root, "data"),
		imagesDir: filepath.Join(root, "data", "images"),
		store:     NewStore(filepath.Join(root, "data")),
		cfg:       Config{ImageRetentionDays: 15},
	}
	webpData := []byte("RIFF\x00\x00\x00\x00WEBPVP8 ")
	rel, url, err := s.saveImageWithBaseURL("https://example.com", webpData)
	if err != nil {
		t.Fatalf("save webp image: %v", err)
	}
	if !strings.HasSuffix(rel, ".webp") {
		t.Fatalf("rel = %q, want .webp suffix", rel)
	}
	if !strings.HasSuffix(url, ".webp") {
		t.Fatalf("url = %q, want .webp suffix", url)
	}
	if _, err := os.Stat(filepath.Join(s.imagesDir, filepath.FromSlash(rel))); err != nil {
		t.Fatalf("saved file missing: %v", err)
	}
	avifData := []byte{0, 0, 0, 24, 'f', 't', 'y', 'p', 'a', 'v', 'i', 'f', 0, 0, 0, 0}
	if got := storedImageExtension(avifData); got != ".avif" {
		t.Fatalf("avif extension = %q, want .avif", got)
	}
	if !isStoredImageFile("image.avif") || !isStoredImageFile("image.gif") {
		t.Fatalf("stored image whitelist should include avif and gif")
	}
}

func TestHandleThumbnailRejectsImageDirectories(t *testing.T) {
	root := t.TempDir()
	s := &Server{
		dataDir:   filepath.Join(root, "data"),
		imagesDir: filepath.Join(root, "data", "images"),
	}
	if err := os.MkdirAll(filepath.Join(s.imagesDir, "2026", "07", "05"), 0755); err != nil {
		t.Fatalf("make image date dir: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/image-thumbnails/2026/07/05/", nil)
	rr := httptest.NewRecorder()
	s.handleThumbnail(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("thumbnail directory status = %d body=%q, want 404", rr.Code, rr.Body.String())
	}
}

func TestHandleWebServesStaticExportRouteIndex(t *testing.T) {
	root := t.TempDir()
	webDist := filepath.Join(root, "web_dist")
	if err := os.MkdirAll(filepath.Join(webDist, "image"), 0755); err != nil {
		t.Fatalf("make image route dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(webDist, "_next", "static"), 0755); err != nil {
		t.Fatalf("make static dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(webDist, "index.html"), []byte("root index"), 0644); err != nil {
		t.Fatalf("write root index: %v", err)
	}
	if err := os.WriteFile(filepath.Join(webDist, "image", "index.html"), []byte("image route"), 0644); err != nil {
		t.Fatalf("write route index: %v", err)
	}
	if err := os.WriteFile(filepath.Join(webDist, "_next", "static", "app.js"), []byte("asset"), 0644); err != nil {
		t.Fatalf("write static asset: %v", err)
	}
	s := &Server{webDist: webDist}

	for _, path := range []string{"/image", "/image/"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rr := httptest.NewRecorder()
		s.handleWeb(rr, req)
		if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "image route") {
			t.Fatalf("%s response status=%d body=%q, want route index", path, rr.Code, rr.Body.String())
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/_next/static/app.js", nil)
	rr := httptest.NewRecorder()
	s.handleWeb(rr, req)
	if rr.Code != http.StatusOK || rr.Body.String() != "asset" {
		t.Fatalf("static asset response status=%d body=%q, want asset", rr.Code, rr.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/missing.js", nil)
	rr = httptest.NewRecorder()
	s.handleWeb(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("missing asset status = %d, want 404", rr.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/../config.json", nil)
	rr = httptest.NewRecorder()
	s.handleWeb(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("path traversal status = %d, want 404", rr.Code)
	}
}

func TestImageTaskCancelRejectsOversizedBody(t *testing.T) {
	t.Setenv("GPT2API_IMAGE_AUTH_KEY", "")
	t.Setenv("GPT2API_IMAGE_DATABASE_URL", "")
	t.Setenv("GPT2API_IMAGE_BASE_URL", "")
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "config.json"), []byte(`{"auth-key":"test","image_retention_days":15}`), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	s, err := newServer(root, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	body := `{"id":"` + strings.Repeat("x", int(maxJSONBodyBytes)+1) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/image-tasks/cancel", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer test")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("cancel status = %d body=%s, want 413", rr.Code, rr.Body.String())
	}
}

func TestImageTaskGenerationRejectsEmptyPromptWithoutDB(t *testing.T) {
	t.Setenv("GPT2API_IMAGE_AUTH_KEY", "")
	t.Setenv("GPT2API_IMAGE_DATABASE_URL", "")
	t.Setenv("GPT2API_IMAGE_BASE_URL", "")
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "config.json"), []byte(`{"auth-key":"test","image_retention_days":15}`), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	s, err := newServer(root, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	s.imageGenerator = func(ctx context.Context, prompt, model, size, resolution string, refs [][]byte, n int) ([]upstreamImageResult, error) {
		t.Fatalf("empty prompt should be rejected before image generation")
		return nil, nil
	}

	req := httptest.NewRequest(http.MethodPost, "/api/image-tasks/generations", strings.NewReader(`{"client_task_id":"empty-prompt","prompt":"   "}`))
	req.Header.Set("Authorization", "Bearer test")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("generation status = %d body=%s, want 400", rr.Code, rr.Body.String())
	}
	if got := s.store.LoadTasks(); len(got) != 0 {
		t.Fatalf("empty prompt should not create a task, got %#v", got)
	}
}

func TestImageTaskTimingSettingsHaveSafeMinimums(t *testing.T) {
	t.Setenv("GPT2API_IMAGE_AUTH_KEY", "")
	t.Setenv("GPT2API_IMAGE_DATABASE_URL", "")
	t.Setenv("GPT2API_IMAGE_BASE_URL", "")
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "config.json"), []byte(`{"auth-key":"test","image_task_timeout_secs":1,"image_task_claim_ttl_secs":1}`), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	s, err := newServer(root, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	if s.cfg.ImageTaskTimeoutSecs != minImageTaskTimeoutSecs {
		t.Fatalf("task timeout = %d, want %d", s.cfg.ImageTaskTimeoutSecs, minImageTaskTimeoutSecs)
	}
	if s.cfg.ImageTaskClaimTTLSecs != minImageTaskClaimTTLSecs {
		t.Fatalf("claim ttl = %d, want %d", s.cfg.ImageTaskClaimTTLSecs, minImageTaskClaimTTLSecs)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/settings", strings.NewReader(`{"image_task_timeout_secs":2,"image_task_claim_ttl_secs":2}`))
	req.Header.Set("Authorization", "Bearer test")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("settings status = %d body=%s, want 200", rr.Code, rr.Body.String())
	}
	if s.cfg.ImageTaskTimeoutSecs != minImageTaskTimeoutSecs {
		t.Fatalf("settings task timeout = %d, want %d", s.cfg.ImageTaskTimeoutSecs, minImageTaskTimeoutSecs)
	}
	if s.cfg.ImageTaskClaimTTLSecs != minImageTaskClaimTTLSecs {
		t.Fatalf("settings claim ttl = %d, want %d", s.cfg.ImageTaskClaimTTLSecs, minImageTaskClaimTTLSecs)
	}
}

func TestAdminManagementJSONEndpointsRejectOversizedBody(t *testing.T) {
	t.Setenv("GPT2API_IMAGE_AUTH_KEY", "")
	t.Setenv("GPT2API_IMAGE_DATABASE_URL", "")
	t.Setenv("GPT2API_IMAGE_BASE_URL", "")
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "config.json"), []byte(`{"auth-key":"test","image_retention_days":15}`), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	s, err := newServer(root, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	if err := s.store.SaveAuthKeys([]UserKey{{ID: "user-a", Name: "user-a", Role: "user", Enabled: true}}); err != nil {
		t.Fatalf("save auth keys: %v", err)
	}
	huge := strings.Repeat("x", int(maxJSONBodyBytes)+1)
	cases := []struct {
		name string
		path string
		body string
	}{
		{name: "accounts refresh", path: "/api/accounts/refresh", body: `{"access_tokens":["` + huge + `"]}`},
		{name: "auth regenerate", path: "/api/auth/users/user-a/regenerate", body: `{"key":"` + huge + `"}`},
		{name: "images download", path: "/api/images/download", body: `{"paths":["` + huge + `"]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer test")
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			s.Handler().ServeHTTP(rr, req)
			if rr.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("%s status = %d body=%s, want 413", tc.path, rr.Code, rr.Body.String())
			}
		})
	}
}

func TestTextV1RoutesDisabledInImageOnlyBuild(t *testing.T) {
	t.Setenv("GPT2API_IMAGE_AUTH_KEY", "")
	t.Setenv("GPT2API_IMAGE_DATABASE_URL", "")
	t.Setenv("GPT2API_IMAGE_BASE_URL", "")
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "config.json"), []byte(`{"auth-key":"test","image_retention_days":15}`), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	s, err := newServer(root, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	cases := []struct {
		name string
		path string
		body string
	}{
		{
			name: "chat completions text",
			path: "/v1/chat/completions",
			body: `{"model":"gpt-5","messages":[{"role":"user","content":"hello"}]}`,
		},
		{
			name: "responses text",
			path: "/v1/responses",
			body: `{"model":"gpt-5","input":"hello"}`,
		},
		{
			name: "messages",
			path: "/v1/messages",
			body: `{"model":"gpt-5","messages":[{"role":"user","content":"hello"}]}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer test")
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			s.Handler().ServeHTTP(rr, req)
			if rr.Code != http.StatusNotFound {
				t.Fatalf("%s status = %d body=%s, want 404", tc.path, rr.Code, rr.Body.String())
			}
		})
	}
}

func TestUnsafeDefaultAuthKeyRejected(t *testing.T) {
	cases := []string{
		"change-me",
		"replace-with-a-long-random-admin-key",
		"<openssl-rand-hex-32>",
	}
	for _, value := range cases {
		t.Run(value, func(t *testing.T) {
			t.Setenv("GPT2API_IMAGE_AUTH_KEY", "")
			t.Setenv("GPT2API_IMAGE_DATABASE_URL", "")
			root := t.TempDir()
			raw, _ := json.Marshal(map[string]any{"auth-key": value})
			if err := os.WriteFile(filepath.Join(root, "config.json"), raw, 0644); err != nil {
				t.Fatalf("write config: %v", err)
			}
			_, err := newServer(root, false)
			if err == nil || !strings.Contains(err.Error(), "默认占位值") {
				t.Fatalf("newServer err = %v, want unsafe default auth-key error", err)
			}
		})
	}
}

func TestUpstreamTransportTLSClientDoesNotRequireCurl(t *testing.T) {
	called := false
	client, err := NewUpstreamClientForAccountWithTransport(Account{AccessToken: "token"}, "", func() (string, error) {
		called = true
		return "", errors.New("curl should not be used")
	}, "tls-client")
	if err != nil {
		t.Fatalf("new tls-client upstream client: %v", err)
	}
	if client == nil {
		t.Fatalf("client is nil")
	}
	if called {
		t.Fatalf("tls-client transport should not request curl-impersonate binary")
	}
}

func TestSystemStatusAndCORSUseConfiguredRuntime(t *testing.T) {
	t.Setenv("GPT2API_IMAGE_AUTH_KEY", "")
	t.Setenv("GPT2API_IMAGE_DATABASE_URL", "")
	t.Setenv("GPT2API_IMAGE_BASE_URL", "")
	root := t.TempDir()
	raw := []byte(`{"auth-key":"test","upstream_transport":"tls-client","cors_allowed_origins":["https://allowed.example"]}`)
	if err := os.WriteFile(filepath.Join(root, "config.json"), raw, 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	s, err := newServer(root, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodOptions, "/api/settings", nil)
	req.Header.Set("Origin", "https://allowed.example")
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("options status = %d, want 204", rr.Code)
	}
	if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "https://allowed.example" {
		t.Fatalf("allow origin = %q, want configured origin", got)
	}

	req = httptest.NewRequest(http.MethodOptions, "/api/settings", nil)
	req.Header.Set("Origin", "https://blocked.example")
	rr = httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("blocked origin got allow header %q", got)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/system/status", nil)
	req.Header.Set("Authorization", "Bearer test")
	rr = httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("system status = %d body=%s, want 200", rr.Code, rr.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if body["transport"] != "tls-client" {
		t.Fatalf("transport = %#v, want tls-client", body["transport"])
	}
	if body["storage"] != "json" {
		t.Fatalf("storage = %#v, want json", body["storage"])
	}
	if body["curl_impersonate_bin"] != "" || body["curl_impersonate_executable"] != false {
		t.Fatalf("tls status should not resolve curl binary: %#v", body)
	}
}

func TestCallLogsHidePromptByDefaultAndRedactSecrets(t *testing.T) {
	s := &Server{
		cfg:        Config{},
		logSvc:     newLogService(t.TempDir()),
		callStarts: map[string]time.Time{},
	}
	callID := s.logCallStart(&Identity{ID: "svc", Role: "admin", Name: "Admin Key"}, "/v1/images/generations", "gpt-image-2", "文生图", "private prompt")
	s.logCallFailure(callID, "/v1/images/generations", "gpt-image-2", "文生图", errors.New(`access_token: abc123 Bearer xyz789 password=secret {"refresh_token":"rrr456"}`), nil)

	successID := s.logCallStart(&Identity{ID: "svc-ok", Role: "admin", Name: "Success Key"}, "/v1/responses", "gpt-image-2", "Responses image", "success prompt")
	s.logCallSuccess(successID, "/v1/responses", "gpt-image-2", "Responses image", map[string]any{"images": 1})

	items := s.logSvc.listFiltered("call", "", "", "", "", "", "", 10)
	if len(items) != 4 {
		t.Fatalf("logs len = %d, want 4", len(items))
	}
	seenFailed := false
	seenSuccess := false
	for _, item := range items {
		detail, _ := item["detail"].(map[string]any)
		if _, ok := detail["request_text"]; ok {
			t.Fatalf("request_text should be disabled by default: %#v", detail)
		}
		if strAny(detail["status"], "") == "failed" {
			seenFailed = true
			if got := strAny(detail["key_name"], ""); got != "Admin Key" {
				t.Fatalf("failed log key_name = %q, want Admin Key; detail=%#v", got, detail)
			}
			if got := strAny(detail["subject_id"], ""); got != "svc" {
				t.Fatalf("failed log subject_id = %q, want svc; detail=%#v", got, detail)
			}
		}
		if strAny(detail["status"], "") == "success" && strAny(detail["endpoint"], "") == "/v1/responses" {
			seenSuccess = true
			if got := strAny(detail["key_name"], ""); got != "Success Key" {
				t.Fatalf("success log key_name = %q, want Success Key; detail=%#v", got, detail)
			}
			if got := strAny(detail["subject_id"], ""); got != "svc-ok" {
				t.Fatalf("success log subject_id = %q, want svc-ok; detail=%#v", got, detail)
			}
		}
		if errText := strAny(detail["error"], ""); errText != "" {
			if strings.Contains(errText, "abc123") || strings.Contains(errText, "xyz789") || strings.Contains(errText, "secret") || strings.Contains(errText, "rrr456") {
				t.Fatalf("error was not redacted: %q", errText)
			}
		}
	}
	if !seenFailed {
		t.Fatalf("failed call log was not found: %#v", items)
	}
	if !seenSuccess {
		t.Fatalf("success call log was not found: %#v", items)
	}
}

func TestV1ModelsImageOnly(t *testing.T) {
	t.Setenv("GPT2API_IMAGE_AUTH_KEY", "")
	t.Setenv("GPT2API_IMAGE_DATABASE_URL", "")
	t.Setenv("GPT2API_IMAGE_BASE_URL", "")
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "config.json"), []byte(`{"auth-key":"test","image_retention_days":15}`), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	s, err := newServer(root, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer test")
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("models status = %d body=%s, want 200", rr.Code, rr.Body.String())
	}
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode models: %v", err)
	}
	if len(body.Data) == 0 {
		t.Fatalf("models list is empty")
	}
	for _, item := range body.Data {
		if !isSupportedImageModel(item.ID) {
			t.Fatalf("model %q is not an image model; full body=%s", item.ID, rr.Body.String())
		}
	}
}
