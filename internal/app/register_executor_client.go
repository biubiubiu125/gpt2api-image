package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

func (s *Server) registerExecutorURL() string {
	return strings.TrimRight(strings.TrimSpace(s.cfg.RegisterExecutorURL), "/")
}

func (s *Server) registerExecutorConfigured() bool {
	return s.registerExecutorURL() != ""
}

func (s *Server) registerExecutorHeaders(req *http.Request) http.Header {
	h := http.Header{}
	if ct := req.Header.Get("Content-Type"); ct != "" {
		h.Set("Content-Type", ct)
	}
	if key := strings.TrimSpace(s.cfg.RegisterInternalKey); key != "" {
		h.Set("X-Register-Internal-Key", key)
		h.Set("Authorization", "Bearer "+key)
		return h
	}
	if key := strings.TrimSpace(s.cfg.AuthKey); key != "" {
		h.Set("Authorization", "Bearer "+key)
	}
	return h
}

func (s *Server) proxyRegisterExecutorJSON(w http.ResponseWriter, r *http.Request, path string) {
	target := s.registerExecutorURL() + path
	var body io.Reader
	if r.Body != nil {
		data, err := io.ReadAll(io.LimitReader(r.Body, maxJSONBodyBytes+1))
		if err != nil {
			writeErr(w, 400, "failed to read request body")
			return
		}
		if int64(len(data)) > maxJSONBodyBytes {
			writeErr(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		body = bytes.NewReader(data)
	}
	ctx, cancel := context.WithTimeout(r.Context(), registerExecutorTimeout(path))
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, r.Method, target, body)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	req.Header = s.registerExecutorHeaders(r)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		writeErr(w, 502, "register executor unavailable: "+err.Error())
		return
	}
	defer resp.Body.Close()
	for key, values := range resp.Header {
		if strings.EqualFold(key, "Content-Length") {
			continue
		}
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func registerExecutorTimeout(path string) time.Duration {
	if path == "/api/register/outlook-pool/test" {
		return 7 * time.Minute
	}
	return 180 * time.Second
}

func (s *Server) proxyRegisterExecutorEvents(w http.ResponseWriter, r *http.Request) {
	target := s.registerExecutorURL() + "/api/register/events"
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, target, nil)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	req.Header = s.registerExecutorHeaders(r)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		writeErr(w, 502, "register executor unavailable: "+err.Error())
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	if resp.StatusCode != http.StatusOK {
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
		return
	}
	w.WriteHeader(http.StatusOK)
	buf := make([]byte, 4096)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, err := w.Write(buf[:n]); err != nil {
				return
			}
			flushSSE(w)
		}
		if readErr != nil {
			return
		}
	}
}

func (s *Server) postRegisterExecutor(path string, body any) (map[string]any, error) {
	payload, _ := json.Marshal(body)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.registerExecutorURL()+path, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header = http.Header{"Content-Type": []string{"application/json"}}
	if key := strings.TrimSpace(s.cfg.RegisterInternalKey); key != "" {
		req.Header.Set("X-Register-Internal-Key", key)
		req.Header.Set("Authorization", "Bearer "+key)
	} else if key := strings.TrimSpace(s.cfg.AuthKey); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return out, fmt.Errorf("register executor http %d", resp.StatusCode)
	}
	return out, nil
}
