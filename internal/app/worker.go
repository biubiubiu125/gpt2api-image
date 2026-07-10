package app

import (
	"context"
	"encoding/base64"
	"errors"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

func RunWorker(ctx context.Context, root string) error {
	s, err := newServer(root, false)
	if err != nil {
		return err
	}
	defer s.taskStore.Close()
	if err := s.validateWorkerConfig(); err != nil {
		return err
	}
	workerID := strings.TrimSpace(os.Getenv("GPT2API_IMAGE_WORKER_ID"))
	if workerID == "" {
		workerID = "worker-" + randID(8)
	}
	concurrency := envInt("GPT2API_IMAGE_WORKER_CONCURRENCY", 4)
	if concurrency < 1 {
		concurrency = 1
	}
	pollInterval := time.Duration(s.cfg.ImageWorkerPollIntervalSecs) * time.Second
	ttl := time.Duration(s.cfg.ImageTaskClaimTTLSecs) * time.Second
	log.Printf("gpt2api-image worker started id=%s concurrency=%d", workerID, concurrency)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.workerExpireLoop(ctx, pollInterval)
	}()
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			s.workerLoop(ctx, workerID, slot, ttl, pollInterval)
		}(i)
	}
	<-ctx.Done()
	wg.Wait()
	return nil
}

func ValidateWorkerConfig(root string) error {
	s, err := newServer(root, false)
	if err != nil {
		return err
	}
	if s.taskStore != nil {
		defer s.taskStore.Close()
	}
	return s.validateWorkerConfig()
}

func (s *Server) validateWorkerConfig() error {
	if s.taskStore == nil {
		return errors.New("GPT2API_IMAGE_DATABASE_URL is required in worker mode")
	}
	if strings.TrimSpace(s.cfg.BaseURL) == "" {
		return errors.New("GPT2API_IMAGE_BASE_URL is required in worker mode")
	}
	return nil
}

func (s *Server) workerLoop(ctx context.Context, workerID string, slot int, ttl time.Duration, pollInterval time.Duration) {
	if pollInterval <= 0 {
		pollInterval = time.Second
	}
	for {
		if ctx.Err() != nil {
			return
		}
		claimID := workerID + "-slot" + strconv.Itoa(slot) + "-" + randID(6)
		task, ok, err := s.taskStore.ClaimTask(ctx, claimID, ttl)
		if err != nil {
			log.Printf("worker claim failed slot=%d error=%v", slot, err)
			sleepContext(ctx, 3*time.Second)
			continue
		}
		if !ok {
			sleepContext(ctx, pollInterval)
			continue
		}
		if err := s.reloadConfigFromDisk(); err != nil {
			log.Printf("worker reload config failed slot=%d error=%v", slot, err)
		}
		if err := s.runDBImageTask(ctx, claimID, ttl, task); err != nil {
			log.Printf("worker task failed id=%s error=%v", task.ID, err)
		}
	}
}

func (s *Server) workerExpireLoop(ctx context.Context, interval time.Duration) {
	if interval < 5*time.Second {
		interval = 5 * time.Second
	}
	if err := s.reloadConfigFromDisk(); err != nil {
		log.Printf("worker reload config failed before expire loop: %v", err)
	}
	s.expireOverdueDBTasks(ctx)
	for sleepContextBool(ctx, interval) {
		s.expireOverdueDBTasks(ctx)
	}
}

func (s *Server) runDBImageTask(parent context.Context, workerID string, ttl time.Duration, task DBImageTask) error {
	deadline := time.Unix(0, int64(task.DeadlineTS*1e9))
	timeout := time.Until(deadline)
	identity := s.taskOwnerIdentity(task.OwnerID, task.OwnerRole)
	endpoint := "/api/image-tasks/generations"
	action := "async image generation"
	if task.Mode == "edit" {
		endpoint = "/api/image-tasks/edits"
		action = "async image edit"
	}
	task.Model = normalizeImageModel(task.Model)
	callID := s.logCallStart(identity, endpoint, task.Model, action, task.Prompt)
	if timeout <= 0 {
		err := errors.New("image task timed out")
		if changed, _ := s.taskStore.FailTask(parent, task.ID, workerID, err.Error()); changed {
			s.refundImage(identity, task.N)
			s.cleanupTaskInputPaths(task.InputPaths)
			s.logCallFailure(callID, endpoint, task.Model, action, err, map[string]any{"task_id": task.ID})
		}
		return err
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	stopHeartbeat := s.startTaskHeartbeat(ctx, cancel, workerID, task.ID, ttl)
	defer stopHeartbeat()
	refs := [][]byte{}
	if task.Mode == "edit" {
		for _, path := range task.InputPaths {
			data, err := os.ReadFile(path)
			if err != nil {
				if changed, _ := s.taskStore.FailTask(parent, task.ID, workerID, err.Error()); changed {
					s.refundImage(identity, maxInt(task.N, 1))
					s.cleanupTaskInputPaths(task.InputPaths)
					s.logCallFailure(callID, endpoint, task.Model, action, err, map[string]any{"task_id": task.ID})
				}
				return err
			}
			refs = append(refs, data)
		}
		if len(refs) == 0 {
			err := errors.New("image file is required")
			if changed, _ := s.taskStore.FailTask(parent, task.ID, workerID, err.Error()); changed {
				s.refundImage(identity, maxInt(task.N, 1))
				s.cleanupTaskInputPaths(task.InputPaths)
				s.logCallFailure(callID, endpoint, task.Model, action, err, map[string]any{"task_id": task.ID})
			}
			return err
		}
	}
	count := task.N
	if count < 1 {
		count = 1
	}
	if err := s.checkImageAccess(identity, task.Model, task.Size, task.Resolution); err != nil {
		if changed, _ := s.taskStore.FailTask(parent, task.ID, workerID, err.Error()); changed {
			s.refundImage(identity, count)
			s.cleanupTaskInputPaths(task.InputPaths)
			s.logCallFailure(callID, endpoint, task.Model, action, err, map[string]any{"task_id": task.ID})
		}
		return err
	}
	items, err := s.generateTaskImagesForIdentity(ctx, identity, task.Prompt, task.Model, task.Size, task.Resolution, refs, count)
	if err != nil {
		if errors.Is(err, errImageAccountBusy) {
			if changed, _ := s.taskStore.RequeueTask(parent, task.ID, workerID, time.Duration(s.cfg.ImageWorkerPollIntervalSecs+1)*time.Second); changed {
				s.logSvc.add("task", "image task requeued", map[string]any{"task_id": task.ID, "reason": err.Error()})
			}
			return err
		}
		if changed, _ := s.taskStore.FailTask(parent, task.ID, workerID, err.Error()); changed {
			s.refundImage(identity, count)
			s.cleanupTaskInputPaths(task.InputPaths)
			s.logCallFailure(callID, endpoint, task.Model, action, err, map[string]any{"task_id": task.ID})
		}
		return err
	}
	baseURL := strings.TrimSpace(s.cfg.BaseURL)
	data := []map[string]any{}
	savedRels := []string{}
	for _, result := range items {
		ok, err := s.taskStore.HeartbeatTask(parent, task.ID, workerID, ttl)
		if err != nil {
			s.cleanupSavedImageResults(savedRels)
			return err
		}
		if !ok {
			s.cleanupSavedImageResults(savedRels)
			return errors.New("task was canceled before saving result")
		}
		rel, url, err := s.saveImageWithBaseURL(baseURL, result.Bytes)
		if err != nil {
			s.cleanupSavedImageResults(savedRels)
			if changed, _ := s.taskStore.FailTask(parent, task.ID, workerID, err.Error()); changed {
				s.refundImage(identity, count)
				s.cleanupTaskInputPaths(task.InputPaths)
				s.logCallFailure(callID, endpoint, task.Model, action, err, map[string]any{"task_id": task.ID})
			}
			return err
		}
		savedRels = append(savedRels, rel)
		if err := s.recordImageMetadata(identity, rel, task.Prompt, task.Mode == "edit", savedRels...); err != nil {
			s.cleanupSavedImageResults(savedRels)
			if changed, _ := s.taskStore.FailTask(parent, task.ID, workerID, err.Error()); changed {
				s.refundImage(identity, count)
				s.cleanupTaskInputPaths(task.InputPaths)
				s.logCallFailure(callID, endpoint, task.Model, action, err, map[string]any{"task_id": task.ID})
			}
			return err
		}
		item := map[string]any{"url": url, "revised_prompt": firstNonEmpty(result.RevisedPrompt, task.Prompt)}
		if task.ResponseFormat == "b64_json" || task.ResponseFormat == "" {
			item["b64_json"] = base64.StdEncoding.EncodeToString(result.Bytes)
		}
		data = append(data, item)
		if len(data) >= count {
			break
		}
	}
	changed, err := s.taskStore.CompleteTask(parent, task.ID, workerID, data)
	if err != nil {
		s.cleanupSavedImageResults(savedRels)
		return err
	}
	if !changed {
		s.cleanupSavedImageResults(savedRels)
		return errors.New("task was canceled or reclaimed before completion")
	}
	if len(data) < count {
		s.refundImage(identity, count-len(data))
	}
	s.cleanupTaskInputPaths(task.InputPaths)
	s.logCallSuccess(callID, endpoint, task.Model, action, map[string]any{"task_id": task.ID, "image_count": len(data), "urls": logImageURLs(data)})
	return nil
}

func (s *Server) startTaskHeartbeat(ctx context.Context, cancel context.CancelFunc, workerID string, taskID string, ttl time.Duration) func() {
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	interval := ttl / 3
	if configured := envInt("GPT2API_IMAGE_WORKER_HEARTBEAT_INTERVAL_SECS", 5); configured > 0 {
		interval = time.Duration(configured) * time.Second
	}
	if interval > ttl/3 {
		interval = ttl / 3
	}
	if interval < 5*time.Second {
		interval = 5 * time.Second
	}
	done := make(chan struct{})
	go func() {
		timer := time.NewTimer(interval)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case <-timer.C:
				ok, err := s.taskStore.HeartbeatTask(context.Background(), taskID, workerID, ttl)
				if err != nil {
					log.Printf("worker heartbeat failed task=%s error=%v", taskID, err)
				}
				if !ok {
					cancel()
					return
				}
				timer.Reset(interval)
			}
		}
	}()
	return func() {
		close(done)
	}
}

func (s *Server) expireOverdueDBTasks(ctx context.Context) {
	tasks, err := s.taskStore.ExpireOverdueTasks(ctx, 50)
	if err != nil {
		log.Printf("worker expire overdue tasks failed error=%v", err)
	} else {
		for _, task := range tasks {
			s.refundImage(&Identity{ID: task.OwnerID, Role: task.OwnerRole}, maxInt(task.N, 1))
			s.cleanupTaskInputPaths(task.InputPaths)
		}
	}
	s.maybeCleanupFinishedDBTasks(ctx)
}

func (s *Server) maybeCleanupFinishedDBTasks(ctx context.Context) {
	days := s.cfg.ImageRetentionDays
	if days <= 0 {
		return
	}
	s.taskCleanupMu.Lock()
	if time.Since(s.lastTaskCleanup) < time.Hour {
		s.taskCleanupMu.Unlock()
		return
	}
	s.lastTaskCleanup = time.Now()
	s.taskCleanupMu.Unlock()
	cutoff := time.Now().AddDate(0, 0, -days)
	tasks, err := s.taskStore.DeleteFinishedTasksBefore(ctx, cutoff, 200)
	if err != nil {
		log.Printf("worker cleanup finished tasks failed error=%v", err)
		return
	}
	for _, task := range tasks {
		s.cleanupTaskInputPaths(task.InputPaths)
	}
	if len(tasks) > 0 && s.logSvc != nil {
		s.logSvc.add("system", "清理旧 DB 图片任务", map[string]any{"removed": len(tasks), "retention_days": days})
	}
}

func envInt(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return value
}

func sleepContext(ctx context.Context, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

func sleepContextBool(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
