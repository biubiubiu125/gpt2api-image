package app

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const maxSafeFileNameLen = 96

func parseTimeAny(value string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t, nil
	}
	if t, err := time.Parse("2006-01-02 15:04:05", value); err == nil {
		return t, nil
	}
	return time.Time{}, errors.New("invalid time")
}

func parseCSV(value string) []string {
	parts := strings.Split(value, ",")
	out := []string{}
	for _, part := range parts {
		if item := strings.TrimSpace(part); item != "" {
			out = append(out, item)
		}
	}
	return out
}

func (s *Server) handleImageTasks(w http.ResponseWriter, r *http.Request) {
	id, ok := s.requireIdentity(w, r)
	if !ok {
		return
	}
	wanted := parseCSV(r.URL.Query().Get("ids"))
	lookupIdentity := scopedTaskIdentity(id, r.URL.Query().Get("owner_id"))
	if s.taskStore != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		dbItems, missing, err := s.taskStore.ListTasks(ctx, lookupIdentity, wanted)
		if err != nil {
			writeTaskStoreError(w, err)
			return
		}
		items := make([]ImageTask, 0, len(dbItems))
		for _, item := range dbItems {
			items = append(items, item.Public())
		}
		writeJSON(w, 200, map[string]any{"items": items, "missing_ids": missing})
		return
	}
	wantMap := map[string]bool{}
	for _, taskID := range wanted {
		wantMap[taskID] = true
	}
	items := []ImageTask{}
	seen := map[string]bool{}
	for _, task := range s.store.LoadTasks() {
		if task.OwnerID != "" && task.OwnerID != id.ID && id.Role != "admin" {
			continue
		}
		if len(wantMap) > 0 && !wantMap[task.ID] {
			continue
		}
		items = append(items, task)
		seen[task.ID] = true
	}
	missing := []string{}
	for _, taskID := range wanted {
		if !seen[taskID] {
			missing = append(missing, taskID)
		}
	}
	writeJSON(w, 200, map[string]any{"items": items, "missing_ids": missing})
}
func (s *Server) saveTask(t ImageTask) {
	s.upsertTask(t)
}
func (s *Server) upsertTask(t ImageTask) {
	t.UpdatedAt = nowISO()
	_ = s.store.UpdateTasks(func(tasks []ImageTask) []ImageTask {
		for i := range tasks {
			if tasks[i].ID == t.ID {
				tasks[i] = t
				return tasks
			}
		}
		return append([]ImageTask{t}, tasks...)
	})
}
func (s *Server) setTaskCancel(id string, cancel context.CancelFunc) {
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	s.taskCancels[id] = cancel
}
func (s *Server) popTaskCancel(id string) context.CancelFunc {
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	cancel := s.taskCancels[id]
	delete(s.taskCancels, id)
	return cancel
}
func (s *Server) recoverUnfinishedTasks() {
	tasks := s.store.LoadTasks()
	changed := false
	for i := range tasks {
		if tasks[i].Status == "running" || tasks[i].Status == "queued" {
			tasks[i].Status = "error"
			tasks[i].Error = "server restarted"
			tasks[i].UpdatedAt = nowISO()
			changed = true
		}
	}
	if changed {
		_ = s.store.SaveTasks(tasks)
		if s.logSvc != nil {
			s.logSvc.add("system", "恢复未完成图片任务", map[string]any{"status": "recovered"})
		}
	}
}

func (s *Server) cleanupOldTasks() {
	days := s.cfg.ImageRetentionDays
	if days <= 0 {
		return
	}
	cutoff := time.Now().AddDate(0, 0, -days)
	tasks := s.store.LoadTasks()
	out := tasks[:0]
	removed := 0
	for _, task := range tasks {
		if task.Status != "success" && task.Status != "error" && task.Status != "canceled" {
			out = append(out, task)
			continue
		}
		updated, err := parseTimeAny(task.UpdatedAt)
		if err != nil || updated.After(cutoff) {
			out = append(out, task)
			continue
		}
		removed++
	}
	if removed > 0 {
		_ = s.store.SaveTasks(out)
		if s.logSvc != nil {
			s.logSvc.add("system", "清理旧图片任务", map[string]any{"removed": removed, "retention_days": days})
		}
	}
}

func (s *Server) updateTaskStatus(id, status, errText string, data []map[string]any) {
	_ = s.store.UpdateTasks(func(tasks []ImageTask) []ImageTask {
		for i := range tasks {
			if tasks[i].ID == id {
				tasks[i].Status = status
				tasks[i].Error = errText
				tasks[i].Data = data
				tasks[i].UpdatedAt = nowISO()
				return tasks
			}
		}
		return tasks
	})
}

func (s *Server) saveTaskInputImages(taskID string, inputs [][]byte) ([]string, error) {
	dir := filepath.Join(s.dataDir, "task_inputs", safeFileName(taskID)+"_"+randID(8))
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(inputs))
	for i, data := range inputs {
		if len(data) == 0 {
			continue
		}
		name := filepath.Join(dir, fmt.Sprintf("image_%d_%s.bin", i, randID(8)))
		if err := os.WriteFile(name, data, 0644); err != nil {
			s.cleanupTaskInputPaths(paths)
			_ = os.RemoveAll(dir)
			return nil, err
		}
		paths = append(paths, name)
	}
	if len(paths) == 0 {
		_ = os.Remove(dir)
	}
	return paths, nil
}

func (s *Server) cleanupTaskInputPaths(paths []string) {
	base, err := filepath.Abs(filepath.Clean(filepath.Join(s.dataDir, "task_inputs")))
	if err != nil {
		return
	}
	dirs := map[string]bool{}
	for _, path := range paths {
		if strings.TrimSpace(path) == "" {
			continue
		}
		target, err := filepath.Abs(filepath.Clean(path))
		if err != nil || !strings.HasPrefix(target, base+string(os.PathSeparator)) {
			continue
		}
		_ = os.Remove(target)
		dir := filepath.Dir(target)
		if dir != base {
			dirs[dir] = true
		}
	}
	for dir := range dirs {
		if dir != base && strings.HasPrefix(dir, base+string(os.PathSeparator)) {
			_ = os.Remove(dir)
		}
	}
}

func scopedTaskIdentity(identity *Identity, ownerID string) Identity {
	out := *identity
	ownerID = strings.TrimSpace(ownerID)
	if out.Role == "admin" && ownerID != "" {
		out.ID = ownerID
		out.Role = "user"
	}
	return out
}

func writeTaskStoreError(w http.ResponseWriter, err error) {
	if errors.Is(err, errAdminClientTaskOwnerRequired) {
		writeErr(w, 400, err.Error())
		return
	}
	writeErr(w, 500, err.Error())
}

func safeFileName(value string) string {
	raw := strings.TrimSpace(value)
	if raw == "" {
		return randID(8)
	}
	var b strings.Builder
	for _, r := range raw {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		sum := hashKey(raw)
		if len(sum) > 12 {
			sum = sum[:12]
		}
		return "id_" + sum
	}
	name := b.String()
	if len(name) <= maxSafeFileNameLen {
		return name
	}
	sum := hashKey(raw)
	if len(sum) > 12 {
		sum = sum[:12]
	}
	prefixLen := maxSafeFileNameLen - len(sum) - 1
	if prefixLen < 1 {
		return sum
	}
	prefix := strings.TrimRight(name[:prefixLen], ".-_")
	if prefix == "" {
		prefix = "id"
	}
	return prefix + "_" + sum
}

func normalizeImageResponseFormat(value string) string {
	if strings.EqualFold(strings.TrimSpace(value), "b64_json") {
		return "b64_json"
	}
	if strings.EqualFold(strings.TrimSpace(value), "url") {
		return "url"
	}
	return "b64_json"
}

func imageTaskID(ownerID, clientTaskID string) string {
	clientTaskID = strings.TrimSpace(clientTaskID)
	if clientTaskID == "" {
		return randID(16)
	}
	sum := hashKey(ownerID + ":" + clientTaskID)
	if len(sum) > 12 {
		sum = sum[:12]
	}
	return "usr_" + sum + "_" + safeFileName(clientTaskID)
}

func possibleImageTaskIDs(ownerID, id string) []string {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil
	}
	out := []string{id}
	derived := imageTaskID(ownerID, id)
	if derived != id {
		out = append(out, derived)
	}
	return out
}

type imageTaskCreateRequest struct {
	ClientTaskID   string
	Mode           string
	Prompt         string
	Model          string
	Size           string
	Resolution     string
	ResponseFormat string
	N              int
	Inputs         [][]byte
}

type statusError struct {
	status int
	err    error
}

func (e statusError) Error() string { return e.err.Error() }

func httpStatusError(status int, format string, args ...any) error {
	return statusError{status: status, err: fmt.Errorf(format, args...)}
}

func writeCreateTaskError(w http.ResponseWriter, err error) {
	var se statusError
	if errors.As(err, &se) {
		writeErr(w, se.status, se.Error())
		return
	}
	writeErr(w, 500, err.Error())
}

func (s *Server) createDBImageTask(ctx context.Context, id *Identity, req imageTaskCreateRequest) (ImageTask, bool, error) {
	if s.taskStore == nil {
		return ImageTask{}, false, httpStatusError(500, "image task store is not enabled")
	}
	req.Prompt = strings.TrimSpace(req.Prompt)
	if req.Prompt == "" {
		return ImageTask{}, false, httpStatusError(400, "prompt is required")
	}
	if req.Model == "" {
		req.Model = "gpt-image-2"
	}
	if req.N < 1 {
		req.N = 1
	}
	if req.N > 4 {
		req.N = 4
	}
	if req.Mode == "" {
		req.Mode = "generate"
	}
	if req.Mode == "edit" {
		req.N = 1
		if len(req.Inputs) == 0 {
			return ImageTask{}, false, httpStatusError(400, "image file is required")
		}
	}
	if err := s.checkContent(req.Prompt); err != nil {
		return ImageTask{}, false, httpStatusError(400, "%s", err.Error())
	}
	if err := s.checkImageAccess(id, req.Model, req.Resolution); err != nil {
		return ImageTask{}, false, err
	}
	taskID := imageTaskID(id.ID, req.ClientTaskID)
	if existing, err := s.taskStore.GetTask(ctx, taskID, *id); err == nil {
		return existing.Public(), false, nil
	}
	if !s.consumeImage(id, req.N) {
		return ImageTask{}, false, httpStatusError(402, "画图额度不足")
	}
	inputPaths := []string{}
	if req.Mode == "edit" {
		var err error
		inputPaths, err = s.saveTaskInputImages(taskID, req.Inputs)
		if err != nil {
			s.refundImage(id, req.N)
			return ImageTask{}, false, err
		}
		if len(inputPaths) == 0 {
			s.refundImage(id, req.N)
			return ImageTask{}, false, httpStatusError(400, "image file is required")
		}
	}
	task, created, err := s.taskStore.CreateTask(ctx, DBImageTask{
		ID:             taskID,
		ClientTaskID:   strings.TrimSpace(req.ClientTaskID),
		OwnerID:        id.ID,
		OwnerRole:      id.Role,
		Status:         dbTaskStatusQueued,
		Mode:           req.Mode,
		Prompt:         req.Prompt,
		Model:          req.Model,
		Size:           req.Size,
		Resolution:     req.Resolution,
		ResponseFormat: normalizeImageResponseFormat(req.ResponseFormat),
		N:              req.N,
		InputPaths:     inputPaths,
		DeadlineTS:     float64(time.Now().UnixNano())/1e9 + float64(s.cfg.ImageTaskTimeoutSecs),
	})
	if err != nil {
		s.refundImage(id, req.N)
		s.cleanupTaskInputPaths(inputPaths)
		return ImageTask{}, false, err
	}
	if !created {
		s.refundImage(id, req.N)
		s.cleanupTaskInputPaths(inputPaths)
	}
	return task.Public(), created, nil
}

func imageTaskResponse(task ImageTask, created bool) map[string]any {
	return map[string]any{
		"id":      task.ID,
		"object":  "image.task",
		"created": created,
		"task":    task,
		"status":  task.Status,
	}
}

func chatCompletionTaskResponse(task ImageTask) map[string]any {
	return map[string]any{
		"id":      "chatcmpl-" + task.ID,
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   task.Model,
		"task":    task,
		"choices": []any{
			map[string]any{
				"index": 0,
				"message": map[string]any{
					"role":    "assistant",
					"content": "Image task queued.",
				},
				"finish_reason": "stop",
			},
		},
	}
}

func responseTaskResponse(task ImageTask) map[string]any {
	created := time.Now().Unix()
	return map[string]any{
		"id":                  "resp_" + task.ID,
		"object":              "response",
		"created_at":          created,
		"status":              "queued",
		"model":               task.Model,
		"output":              []any{},
		"parallel_tool_calls": false,
		"task":                task,
	}
}

func chatStreamTaskResponse(w http.ResponseWriter, task ImageTask) {
	w.Header().Set("Content-Type", "text/event-stream")
	sse(w, map[string]any{"type": "image_task.queued", "task": task, "done": true})
	sseDone(w)
}

func (s *Server) readMultipartImageInputs(r *http.Request) ([][]byte, error) {
	inputs := [][]byte{}
	if r.MultipartForm == nil {
		return inputs, nil
	}
	for _, key := range []string{"image", "image[]"} {
		for _, fh := range r.MultipartForm.File[key] {
			f, err := fh.Open()
			if err != nil {
				continue
			}
			b, readErr := readAllLimited(f, maxMultipartBodyBytes)
			_ = f.Close()
			if readErr != nil {
				return nil, readErr
			}
			if len(b) > 0 {
				inputs = append(inputs, b)
			}
		}
	}
	return inputs, nil
}

func (s *Server) enqueueV1ImageTask(w http.ResponseWriter, r *http.Request, id *Identity, req imageTaskCreateRequest) bool {
	if s.taskStore == nil || req.Mode == "" || strings.TrimSpace(req.ClientTaskID) == "" {
		return false
	}
	task, created, err := s.createDBImageTask(r.Context(), id, req)
	if err != nil {
		writeCreateTaskError(w, err)
		return true
	}
	writeJSON(w, 202, imageTaskResponse(task, created))
	return true
}

func (s *Server) handleImageTaskGeneration(w http.ResponseWriter, r *http.Request) {
	id, ok := s.requireIdentity(w, r)
	if !ok {
		return
	}
	var b struct {
		ClientTaskID   string `json:"client_task_id"`
		Prompt         string `json:"prompt"`
		Model          string `json:"model"`
		N              int    `json:"n"`
		Size           string `json:"size"`
		Resolution     string `json:"resolution"`
		ResponseFormat string `json:"response_format"`
	}
	if !readBody(w, r, &b) {
		return
	}
	if b.N < 1 {
		b.N = 1
	}
	if b.N > 4 {
		b.N = 4
	}
	if s.taskStore != nil {
		task, _, err := s.createDBImageTask(r.Context(), id, imageTaskCreateRequest{
			ClientTaskID:   b.ClientTaskID,
			Mode:           "generate",
			Prompt:         b.Prompt,
			Model:          b.Model,
			Size:           b.Size,
			Resolution:     b.Resolution,
			ResponseFormat: b.ResponseFormat,
			N:              b.N,
		})
		if err != nil {
			writeCreateTaskError(w, err)
			return
		}
		writeJSON(w, 200, task)
		return
	}
	t := ImageTask{ID: firstNonEmpty(strings.TrimSpace(b.ClientTaskID), randID(8)), OwnerID: id.ID, Status: "running", Mode: "generate", Model: b.Model, Size: b.Size, Resolution: b.Resolution, CreatedAt: nowISO(), UpdatedAt: nowISO()}
	callID := s.logCallStart(id, "/api/image-tasks/generations", b.Model, "文生图任务", b.Prompt)
	if err := s.checkContent(b.Prompt); err != nil {
		t.Status = "error"
		t.Error = err.Error()
		s.logCallFailure(callID, "/api/image-tasks/generations", b.Model, "文生图任务", err, nil)
		s.saveTask(t)
		writeJSON(w, 200, t)
		return
	}
	if err := s.checkImageAccess(id, b.Model, b.Resolution); err != nil {
		t.Status = "error"
		t.Error = err.Error()
		s.logCallFailure(callID, "/api/image-tasks/generations", b.Model, "文生图任务", err, nil)
		s.saveTask(t)
		writeJSON(w, 200, t)
		return
	}
	if !s.consumeImage(id, b.N) {
		t.Status = "error"
		t.Error = "画图额度不足"
		s.saveTask(t)
		writeJSON(w, 200, t)
		return
	}
	s.saveTask(t)
	ctx, cancel := context.WithTimeout(context.Background(), s.imageRequestTimeout())
	s.setTaskCancel(t.ID, cancel)
	go func(task ImageTask, identity *Identity) {
		defer s.popTaskCancel(task.ID)
		defer cancel()
		data := []map[string]any{}
		items, err := s.generateImagesWithPool(ctx, b.Prompt, b.Model, b.Size, b.Resolution, nil, b.N)
		if err != nil {
			s.refundImage(identity, b.N)
			if ctx.Err() != nil {
				s.updateTaskStatus(task.ID, "canceled", "canceled", nil)
				s.logCallFailure(callID, "/api/image-tasks/generations", b.Model, "文生图任务", errors.New("canceled"), map[string]any{"task_id": task.ID})
			} else {
				s.updateTaskStatus(task.ID, "error", err.Error(), nil)
				s.logCallFailure(callID, "/api/image-tasks/generations", b.Model, "文生图任务", err, map[string]any{"task_id": task.ID})
			}
			return
		}
		for _, res := range items {
			rel, url, err := s.saveImage(r, res.Bytes)
			if err != nil {
				s.refundImage(identity, b.N-len(data))
				s.updateTaskStatus(task.ID, "error", err.Error(), nil)
				return
			}
			s.recordOwner(identity, rel)
			s.recordPrompt(rel, b.Prompt, false)
			data = append(data, map[string]any{"url": url, "b64_json": base64.StdEncoding.EncodeToString(res.Bytes), "revised_prompt": firstNonEmpty(res.RevisedPrompt, b.Prompt)})
		}
		if len(data) < b.N {
			s.refundImage(identity, b.N-len(data))
		}
		s.updateTaskStatus(task.ID, "success", "", data)
		s.logCallSuccess(callID, "/api/image-tasks/generations", b.Model, "文生图任务", map[string]any{"task_id": task.ID, "image_count": len(data)})
	}(t, id)
	writeJSON(w, 200, t)
}
func (s *Server) handleImageTaskEdit(w http.ResponseWriter, r *http.Request) {
	id, ok := s.requireIdentity(w, r)
	if !ok {
		return
	}
	if !parseMultipartFormLimited(w, r) {
		return
	}
	prompt := r.FormValue("prompt")
	model := r.FormValue("model")
	if model == "" {
		model = "gpt-image-2"
	}
	if s.taskStore != nil {
		inputs, err := s.readMultipartImageInputs(r)
		if err != nil {
			writeErr(w, http.StatusRequestEntityTooLarge, err.Error())
			return
		}
		task, _, err := s.createDBImageTask(r.Context(), id, imageTaskCreateRequest{
			ClientTaskID:   r.FormValue("client_task_id"),
			Mode:           "edit",
			Prompt:         prompt,
			Model:          model,
			Size:           r.FormValue("size"),
			Resolution:     r.FormValue("resolution"),
			ResponseFormat: r.FormValue("response_format"),
			N:              1,
			Inputs:         inputs,
		})
		if err != nil {
			writeCreateTaskError(w, err)
			return
		}
		writeJSON(w, 200, task)
		return
	}
	t := ImageTask{ID: firstNonEmpty(strings.TrimSpace(r.FormValue("client_task_id")), randID(8)), OwnerID: id.ID, Status: "running", Mode: "edit", Model: model, Size: r.FormValue("size"), Resolution: r.FormValue("resolution"), CreatedAt: nowISO(), UpdatedAt: nowISO()}
	callID := s.logCallStart(id, "/api/image-tasks/edits", model, "图生图任务", prompt)
	if err := s.checkContent(prompt); err != nil {
		t.Status = "error"
		t.Error = err.Error()
		s.logCallFailure(callID, "/api/image-tasks/edits", model, "图生图任务", err, nil)
		s.saveTask(t)
		writeJSON(w, 200, t)
		return
	}
	if err := s.checkImageAccess(id, model, r.FormValue("resolution")); err != nil {
		t.Status = "error"
		t.Error = err.Error()
		s.logCallFailure(callID, "/api/image-tasks/edits", model, "图生图任务", err, nil)
		s.saveTask(t)
		writeJSON(w, 200, t)
		return
	}
	if !s.consumeImage(id, 1) {
		t.Status = "error"
		t.Error = "画图额度不足"
		s.saveTask(t)
		writeJSON(w, 200, t)
		return
	}
	inputs := [][]byte{}
	for _, key := range []string{"image", "image[]"} {
		for _, fh := range r.MultipartForm.File[key] {
			f, err := fh.Open()
			if err != nil {
				continue
			}
			b, readErr := readAllLimited(f, maxMultipartBodyBytes)
			_ = f.Close()
			if readErr != nil {
				s.refundImage(id, 1)
				writeErr(w, http.StatusRequestEntityTooLarge, readErr.Error())
				return
			}
			if len(b) > 0 {
				inputs = append(inputs, b)
			}
		}
	}
	if len(inputs) == 0 {
		s.refundImage(id, 1)
		t.Status = "error"
		t.Error = "image file is required"
		s.saveTask(t)
		writeJSON(w, 200, t)
		return
	}
	s.saveTask(t)
	ctx, cancel := context.WithTimeout(context.Background(), s.imageRequestTimeout())
	s.setTaskCancel(t.ID, cancel)
	go func(task ImageTask, identity *Identity) {
		defer s.popTaskCancel(task.ID)
		defer cancel()
		items, err := s.generateImageWithPool(ctx, prompt, model, r.FormValue("size"), r.FormValue("resolution"), inputs)
		if err != nil {
			s.refundImage(identity, 1)
			if ctx.Err() != nil {
				s.updateTaskStatus(task.ID, "canceled", "canceled", nil)
				s.logCallFailure(callID, "/api/image-tasks/edits", model, "图生图任务", errors.New("canceled"), map[string]any{"task_id": task.ID})
			} else {
				s.updateTaskStatus(task.ID, "error", err.Error(), nil)
				s.logCallFailure(callID, "/api/image-tasks/edits", model, "图生图任务", err, map[string]any{"task_id": task.ID})
			}
			return
		}
		data := []map[string]any{}
		for _, res := range items {
			rel, url, err := s.saveImage(r, res.Bytes)
			if err != nil {
				s.refundImage(identity, 1)
				s.updateTaskStatus(task.ID, "error", err.Error(), nil)
				return
			}
			s.recordOwner(identity, rel)
			s.recordPrompt(rel, prompt, true)
			data = append(data, map[string]any{"url": url, "b64_json": base64.StdEncoding.EncodeToString(res.Bytes), "revised_prompt": firstNonEmpty(res.RevisedPrompt, prompt)})
			break
		}
		s.updateTaskStatus(task.ID, "success", "", data)
		s.logCallSuccess(callID, "/api/image-tasks/edits", model, "图生图任务", map[string]any{"task_id": task.ID, "image_count": len(data)})
	}(t, id)
	writeJSON(w, 200, t)
}
func (s *Server) handleImageTaskCancel(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.requireIdentity(w, r)
	if !ok {
		return
	}
	var b map[string]any
	if !readBody(w, r, &b) {
		return
	}
	ids := []string{}
	if arr, ok := b["ids"].([]any); ok {
		for _, item := range arr {
			if v := strings.TrimSpace(strAny(item, "")); v != "" {
				ids = append(ids, v)
			}
		}
	}
	if v := strings.TrimSpace(strAny(b["id"], "")); v != "" {
		ids = append(ids, v)
	}
	lookupIdentity := scopedTaskIdentity(identity, strAny(b["owner_id"], ""))
	canceled := []string{}
	skipped := []string{}
	missing := []string{}
	if s.taskStore != nil {
		for _, taskID := range ids {
			found := false
			for _, candidate := range possibleImageTaskIDs(lookupIdentity.ID, taskID) {
				task, ok, err := s.taskStore.CancelTask(r.Context(), candidate, lookupIdentity)
				if err != nil {
					writeTaskStoreError(w, err)
					return
				}
				if ok {
					s.refundImage(&Identity{ID: task.OwnerID, Role: task.OwnerRole}, maxInt(task.N, 1))
					s.cleanupTaskInputPaths(task.InputPaths)
					canceled = append(canceled, task.ID)
					found = true
					break
				}
			}
			if !found && canUseClientTaskIDFallback(lookupIdentity) {
				tasks, err := s.taskStore.CancelTasksByClientTaskID(r.Context(), taskID, lookupIdentity)
				if err != nil {
					writeTaskStoreError(w, err)
					return
				}
				for _, task := range tasks {
					s.refundImage(&Identity{ID: task.OwnerID, Role: task.OwnerRole}, maxInt(task.N, 1))
					s.cleanupTaskInputPaths(task.InputPaths)
					canceled = append(canceled, task.ID)
					found = true
				}
			}
			if !found {
				missing = append(missing, taskID)
			}
		}
		writeJSON(w, 200, map[string]any{"canceled": canceled, "skipped": skipped, "missing_ids": missing})
		return
	}
	tasks := s.store.LoadTasks()
	byID := map[string]int{}
	for i, t := range tasks {
		byID[t.ID] = i
	}
	for _, id := range ids {
		idx, ok := byID[id]
		if !ok {
			missing = append(missing, id)
			continue
		}
		if tasks[idx].OwnerID != "" && tasks[idx].OwnerID != identity.ID && identity.Role != "admin" {
			skipped = append(skipped, id)
			continue
		}
		if tasks[idx].Status != "running" && tasks[idx].Status != "queued" && tasks[idx].Status != "pending" {
			skipped = append(skipped, id)
			continue
		}
		if cancel := s.popTaskCancel(id); cancel != nil {
			cancel()
		}
		tasks[idx].Status = "canceled"
		tasks[idx].Error = "canceled"
		tasks[idx].UpdatedAt = nowISO()
		canceled = append(canceled, id)
	}
	_ = s.store.SaveTasks(tasks)
	writeJSON(w, 200, map[string]any{"canceled": canceled, "skipped": skipped, "missing_ids": missing})
}

func (s *Server) handleChatStream(w http.ResponseWriter, r *http.Request) {
	id, ok := s.requireIdentity(w, r)
	if !ok {
		return
	}
	var b map[string]any
	if !readBody(w, r, &b) {
		return
	}
	model := strAny(b["model"], "auto")
	requestText := extractPrompt(b)
	callID := s.logCallStart(id, "/api/chat/stream", model, "聊天", requestText)
	if isImageChatRequest(b) {
		s.handleChatStreamImage(w, r, id, b, callID)
		return
	}
	if err := s.checkContent(requestText); err != nil {
		s.logCallFailure(callID, "/api/chat/stream", model, "聊天", err, nil)
		writeErr(w, 400, err.Error())
		return
	}
	if !s.consumeChat(id, 1) {
		writeErr(w, 402, "对话额度不足")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.imageRequestTimeout())
	defer cancel()
	requestedUpstreamCID := strings.TrimSpace(strAny(b["upstream_conversation_id"], strAny(b["conversation_id"], "")))
	_ = requestedUpstreamCID
	upstreamCID := ""
	messages, err := s.messagesFromBodyWithFiles(b)
	if err != nil {
		s.refundChat(id, 1)
		s.logCallFailure(callID, "/api/chat/stream", model, "聊天", err, nil)
		writeErr(w, 400, err.Error())
		return
	}
	events, errs := s.streamChatWithRetry(ctx, messages, model, "", "")
	w.Header().Set("Content-Type", "text/event-stream")
	text := ""
	cid := "conv_" + randID(8)
	upstreamMessageID := ""
	currentNode := ""
	accountToken := ""
	fileIDs := []string{}
	sedimentIDs := []string{}
	toolInvoked := any(nil)
	turnUseCase := ""
	blocked := false
	for ev := range events {
		if ev.ConversationID != "" {
			upstreamCID = ev.ConversationID
		}
		if ev.MessageID != "" {
			upstreamMessageID = ev.MessageID
		}
		if ev.CurrentNode != "" {
			currentNode = ev.CurrentNode
		}
		if ev.AccountToken != "" {
			accountToken = ev.AccountToken
		}
		fileIDs = unique(append(fileIDs, ev.FileIDs...))
		sedimentIDs = unique(append(sedimentIDs, ev.SedimentIDs...))
		if ev.ToolInvoked != nil {
			toolInvoked = ev.ToolInvoked
		}
		if ev.TurnUseCase != "" {
			turnUseCase = ev.TurnUseCase
		}
		if ev.Blocked {
			blocked = true
		}
		if ev.Delta == "" {
			continue
		}
		text += ev.Delta
		sse(w, map[string]any{"type": "conversation.delta", "delta": ev.Delta, "text": text, "conversation_id": firstNonEmpty(upstreamCID, cid), "upstream_conversation_id": upstreamCID, "message_id": upstreamMessageID, "current_node": currentNode, "file_ids": fileIDs, "sediment_ids": sedimentIDs, "blocked": ev.Blocked, "tool_invoked": ev.ToolInvoked, "turn_use_case": ev.TurnUseCase, "done": false})
	}
	if err := <-errs; err != nil {
		s.refundChat(id, 1)
		s.logCallFailure(callID, "/api/chat/stream", model, "聊天", err, map[string]any{"stream": true})
		sse(w, map[string]any{"type": "conversation.error", "error": err.Error(), "done": true})
		sseDone(w)
		return
	}
	doneCID := firstNonEmpty(upstreamCID, cid)
	if doneCID != "" && accountToken != "" && (len(fileIDs) > 0 || len(sedimentIDs) > 0 || toolInvoked == true) {
		imageText, ok := s.resolveChatStreamImages(ctx, r, id, accountToken, doneCID, fileIDs, sedimentIDs, text, len(extractChatImages(b)) > 0)
		if ok {
			text = imageText
			sse(w, map[string]any{"type": "conversation.delta", "delta": imageText, "text": imageText, "conversation_id": doneCID, "upstream_conversation_id": doneCID, "message_id": upstreamMessageID, "current_node": currentNode, "file_ids": fileIDs, "sediment_ids": sedimentIDs, "blocked": blocked, "tool_invoked": toolInvoked, "turn_use_case": turnUseCase, "done": false})
		}
	}
	s.upsertChatConversationFromStream(id, b, doneCID, upstreamMessageID, currentNode, "", text)
	s.logCallSuccess(callID, "/api/chat/stream", model, "聊天", map[string]any{"stream": true, "output_tokens": approxTokens(text), "file_ids": fileIDs, "sediment_ids": sedimentIDs})
	sse(w, map[string]any{"type": "conversation.done", "text": text, "conversation_id": doneCID, "upstream_conversation_id": doneCID, "message_id": upstreamMessageID, "current_node": currentNode, "file_ids": fileIDs, "sediment_ids": sedimentIDs, "blocked": blocked, "tool_invoked": toolInvoked, "turn_use_case": turnUseCase, "done": true})
	sseDone(w)
}
func (s *Server) resolveChatStreamImages(ctx context.Context, r *http.Request, id *Identity, token, conversationID string, fileIDs, sedimentIDs []string, fallbackText string, isEdit bool) (string, bool) {
	client, err := NewUpstreamClientForAccount(s.accountByToken(token), s.cfg.Proxy, s.ensureCurlImpersonateBinary)
	if err != nil {
		return fallbackText, false
	}
	if len(fileIDs) == 0 && len(sedimentIDs) == 0 && conversationID != "" {
		opts := s.imageGenerationOptions()
		f, sed, err := client.pollImageIDs(ctx, conversationID, opts.Timeout, opts.PollInterval, opts.PollInitialWait)
		if err != nil {
			return fallbackText, false
		}
		fileIDs = append(fileIDs, f...)
		sedimentIDs = append(sedimentIDs, sed...)
	}
	urls, err := client.resolveImageURLs(ctx, conversationID, fileIDs, sedimentIDs)
	if err != nil || len(urls) == 0 {
		return fallbackText, false
	}
	data := []map[string]any{}
	for _, u := range urls {
		bytes, err := client.download(ctx, u)
		if err != nil {
			continue
		}
		rel, url, err := s.saveImage(r, bytes)
		if err != nil {
			continue
		}
		s.recordOwner(id, rel)
		s.recordPrompt(rel, fallbackText, isEdit)
		data = append(data, map[string]any{"url": url, "b64_json": base64.StdEncoding.EncodeToString(bytes), "revised_prompt": fallbackText})
	}
	if len(data) == 0 {
		return fallbackText, false
	}
	return buildChatImageMarkdown(data), true
}

func (s *Server) handleChatStreamImage(w http.ResponseWriter, r *http.Request, id *Identity, b map[string]any, callID string) {
	model := strAny(b["model"], "gpt-image-2")
	if !isSupportedImageModel(model) {
		model = "gpt-image-2"
	}
	prompt := extractChatPrompt(b)
	if prompt == "" {
		err := errors.New("prompt is required")
		s.logCallFailure(callID, "/api/chat/stream", model, "聊天", err, nil)
		writeErr(w, 400, err.Error())
		return
	}
	if err := s.checkContent(prompt); err != nil {
		s.logCallFailure(callID, "/api/chat/stream", model, "聊天", err, nil)
		writeErr(w, 400, err.Error())
		return
	}
	if err := s.checkImageAccess(id, model, strAny(b["resolution"], "")); err != nil {
		s.logCallFailure(callID, "/api/chat/stream", model, "聊天", err, map[string]any{"image": true})
		writeErr(w, 403, err.Error())
		return
	}
	if s.taskStore != nil {
		refs := extractChatImages(b)
		mode := "generate"
		if len(refs) > 0 {
			mode = "edit"
		}
		task, _, err := s.createDBImageTask(r.Context(), id, imageTaskCreateRequest{
			ClientTaskID:   strAny(b["client_task_id"], strAny(b["task_id"], "")),
			Mode:           mode,
			Prompt:         prompt,
			Model:          model,
			Size:           strAny(b["size"], ""),
			Resolution:     strAny(b["resolution"], ""),
			ResponseFormat: "b64_json",
			N:              1,
			Inputs:         refs,
		})
		if err != nil {
			s.logCallFailure(callID, "/api/chat/stream", model, "聊天", err, map[string]any{"image": true})
			writeCreateTaskError(w, err)
			return
		}
		s.logCallSuccess(callID, "/api/chat/stream", model, "聊天", map[string]any{"image": true, "task_id": task.ID, "queued": true})
		chatStreamTaskResponse(w, task)
		return
	}
	if !s.consumeImage(id, 1) {
		writeErr(w, 402, "画图额度不足")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.imageRequestTimeout())
	defer cancel()
	items, err := s.generateImageWithPool(ctx, prompt, model, strAny(b["size"], ""), strAny(b["resolution"], ""), extractChatImages(b))
	w.Header().Set("Content-Type", "text/event-stream")
	cid := "conv_" + randID(8)
	if err != nil {
		s.refundImage(id, 1)
		s.logCallFailure(callID, "/api/chat/stream", model, "聊天", err, map[string]any{"image": true})
		sse(w, map[string]any{"type": "conversation.error", "error": err.Error(), "done": true})
		sseDone(w)
		return
	}
	data := []map[string]any{}
	for _, res := range items {
		rel, url, err := s.saveImage(r, res.Bytes)
		if err != nil {
			s.refundImage(id, 1)
			sse(w, map[string]any{"type": "conversation.error", "error": err.Error(), "done": true})
			sseDone(w)
			return
		}
		s.recordOwner(id, rel)
		s.recordPrompt(rel, prompt, len(extractChatImages(b)) > 0)
		data = append(data, map[string]any{"url": url, "b64_json": base64.StdEncoding.EncodeToString(res.Bytes), "revised_prompt": firstNonEmpty(res.RevisedPrompt, prompt)})
		break
	}
	text := buildChatImageMarkdown(data)
	s.logCallSuccess(callID, "/api/chat/stream", model, "聊天", map[string]any{"image": true, "image_count": len(data)})
	sse(w, map[string]any{"type": "conversation.delta", "delta": text, "text": text, "conversation_id": cid, "done": false})
	sse(w, map[string]any{"type": "conversation.done", "text": text, "conversation_id": cid, "done": true})
	sseDone(w)
}

func (s *Server) upsertChatConversationFromStream(id *Identity, b map[string]any, upstreamCID, messageID, currentNode, token, assistantText string) {
	convID := strings.TrimSpace(strAny(b["id"], strAny(b["conversation_local_id"], "")))
	if convID == "" {
		return
	}
	item := map[string]any{}
	for k, v := range b {
		item[k] = v
	}
	item["id"] = convID
	item["owner_id"] = id.ID
	item["upstream_conversation_id"] = upstreamCID
	item["upstream_message_id"] = messageID
	item["current_node"] = currentNode
	item["upstream_account_token"] = token
	item["updated_at"] = nowISO()
	if item["created_at"] == nil || strings.TrimSpace(strAny(item["created_at"], "")) == "" {
		item["created_at"] = nowISO()
	}
	if strings.TrimSpace(strAny(item["title"], "")) == "" {
		item["title"] = truncateText(extractPrompt(b), 40)
	}
	if assistantText != "" {
		item["last_text"] = truncateText(assistantText, 500)
	}
	_ = s.store.UpdateList("chat_conversations.json", func(items []map[string]any) []map[string]any {
		out := []map[string]any{item}
		for _, it := range items {
			if strAny(it["id"], "") != convID {
				out = append(out, it)
			}
		}
		return out
	})
}

func (s *Server) handleChatAccountTypes(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireIdentity(w, r); !ok {
		return
	}
	types := map[string]bool{}
	for _, a := range s.store.LoadAccounts() {
		types[a.Type] = true
	}
	arr := []map[string]any{}
	keys := make([]string, 0, len(types))
	for t := range types {
		if t != "" {
			keys = append(keys, t)
		}
	}
	sort.Strings(keys)
	for _, t := range keys {
		arr = append(arr, map[string]any{"type": t, "label": t})
	}
	if len(arr) == 0 {
		arr = []map[string]any{{"type": "free", "label": "free"}}
	}
	writeJSON(w, 200, map[string]any{"items": arr})
}
func (s *Server) handleChatConversations(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.requireIdentity(w, r)
	if !ok {
		return
	}
	if r.Method == http.MethodGet {
		items := []map[string]any{}
		for _, it := range s.store.LoadList("chat_conversations.json") {
			if strAny(it["owner_id"], identity.ID) == identity.ID || identity.Role == "admin" {
				items = append(items, it)
			}
		}
		writeJSON(w, 200, map[string]any{"items": items})
		return
	}
	if r.Method == http.MethodPost {
		var b map[string]any
		if !readBody(w, r, &b) {
			return
		}
		if strings.TrimSpace(strAny(b["id"], "")) == "" {
			b["id"] = "conv_" + randID(8)
		}
		b["owner_id"] = identity.ID
		b["updated_at"] = nowISO()
		if strings.TrimSpace(strAny(b["created_at"], "")) == "" {
			b["created_at"] = nowISO()
		}
		conflict := false
		_ = s.store.UpdateList("chat_conversations.json", func(items []map[string]any) []map[string]any {
			out := []map[string]any{b}
			for _, it := range items {
				if strAny(it["id"], "") == strAny(b["id"], "") {
					owner := strAny(it["owner_id"], "")
					if owner != "" && owner != identity.ID && identity.Role != "admin" {
						conflict = true
						return items
					}
					continue
				}
				out = append(out, it)
			}
			return out
		})
		if conflict {
			writeErr(w, 403, "不能覆盖其他用户的会话")
			return
		}
		writeJSON(w, 200, map[string]any{"item": b})
		return
	}
	writeErr(w, 405, "method not allowed")
}
func (s *Server) handleChatConversationID(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.requireIdentity(w, r)
	if !ok {
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/chat/conversations/")
	items := s.store.LoadList("chat_conversations.json")
	out := []map[string]any{}
	deleted := false
	for _, it := range items {
		owner := strAny(it["owner_id"], "")
		if strAny(it["id"], "") == id && (owner == "" || owner == identity.ID || identity.Role == "admin") {
			deleted = true
			upstreamCID := strings.TrimSpace(strAny(it["upstream_conversation_id"], ""))
			upstreamToken := strings.TrimSpace(strAny(it["upstream_account_token"], ""))
			if upstreamCID != "" && upstreamToken != "" {
				go func(cid, token string) {
					ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					defer cancel()
					if c, err := NewUpstreamClientForAccount(s.accountByToken(token), s.cfg.Proxy, s.ensureCurlImpersonateBinary); err == nil {
						c.DeleteConversation(ctx, cid)
					}
				}(upstreamCID, upstreamToken)
			}
			continue
		}
		out = append(out, it)
	}
	_ = s.store.SaveList("chat_conversations.json", out)
	writeJSON(w, 200, map[string]any{"ok": deleted})
}

func (s *Server) handleWeb(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		path := filepath.Join(s.webDist, filepath.Clean(r.URL.Path))
		if st, err := os.Stat(path); err == nil && !st.IsDir() {
			http.ServeFile(w, r, path)
			return
		}
	}
	index := filepath.Join(s.webDist, "index.html")
	if _, err := os.Stat(index); err == nil {
		http.ServeFile(w, r, index)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "service": "gpt2api-image", "hint": "web_dist/index.html not found; build frontend to enable SPA"})
}
