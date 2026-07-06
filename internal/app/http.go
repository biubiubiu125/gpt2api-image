package app

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"
)

const (
	maxJSONBodyBytes      int64 = 32 << 20
	maxMultipartBodyBytes int64 = 80 << 20
	maxMultipartMemory          = 32 << 20
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"detail": map[string]any{"error": msg}})
}
func methodNotAllowed(w http.ResponseWriter, allow string) {
	if strings.TrimSpace(allow) != "" {
		w.Header().Set("Allow", allow)
	}
	writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
}
func readBody[T any](w http.ResponseWriter, r *http.Request, dst *T) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) || strings.Contains(err.Error(), "request body too large") {
			writeErr(w, http.StatusRequestEntityTooLarge, "request body too large")
			return false
		}
		writeErr(w, 400, "invalid json body")
		return false
	}
	return true
}

func parseMultipartFormLimited(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxMultipartBodyBytes)
	if err := r.ParseMultipartForm(maxMultipartMemory); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) || strings.Contains(err.Error(), "request body too large") {
			writeErr(w, http.StatusRequestEntityTooLarge, "request body too large")
			return false
		}
		writeErr(w, 400, "invalid multipart form")
		return false
	}
	return true
}

func readAllLimited(r io.Reader, limit int64) ([]byte, error) {
	if limit <= 0 {
		limit = maxMultipartBodyBytes
	}
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("image file too large")
	}
	return data, nil
}
func randID(n int) string { b := make([]byte, n); _, _ = rand.Read(b); return hex.EncodeToString(b) }
func uuid4() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
func clean(s string) string    { return strings.TrimSpace(s) }
func relClean(s string) string { return strings.TrimLeft(filepath.ToSlash(strings.TrimSpace(s)), "/") }
func contains[T comparable](items []T, v T) bool {
	for _, it := range items {
		if it == v {
			return true
		}
	}
	return false
}
func sse(w http.ResponseWriter, event any) {
	b, _ := json.Marshal(event)
	fmt.Fprintf(w, "data: %s\n\n", b)
	flushSSE(w)
}
func sseDone(w http.ResponseWriter) {
	fmt.Fprint(w, "data: [DONE]\n\n")
	flushSSE(w)
}
func sseEvent(w http.ResponseWriter, name string, event any) {
	b, _ := json.Marshal(event)
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, b)
	flushSSE(w)
}
func flushSSE(w http.ResponseWriter) {
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}
func todayKey() string { return time.Now().Format("2006-01-02") }
func monthKey() string { return time.Now().Format("2006-01") }
func intAny(v any, def int) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case int:
		return x
	case json.Number:
		i, _ := x.Int64()
		return int(i)
	default:
		return def
	}
}
func boolAny(v any, def bool) bool {
	if v == nil {
		return def
	}
	b, ok := v.(bool)
	if ok {
		return b
	}
	return def
}
func strAny(v any, def string) string {
	if v == nil {
		return def
	}
	return fmt.Sprint(v)
}
func stringSliceAny(v any) []string {
	switch x := v.(type) {
	case []string:
		out := make([]string, 0, len(x))
		for _, item := range x {
			if s := strings.TrimSpace(item); s != "" {
				out = append(out, s)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(x))
		for _, item := range x {
			if s := strings.TrimSpace(strAny(item, "")); s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		if s := strings.TrimSpace(strAny(v, "")); s != "" {
			return []string{s}
		}
		return []string{}
	}
}
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
func messageTextAny(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []any:
		var parts []string
		for _, item := range v {
			switch x := item.(type) {
			case string:
				parts = append(parts, x)
			case map[string]any:
				t := strings.TrimSpace(strAny(x["type"], ""))
				switch t {
				case "", "text", "input_text", "output_text":
					parts = append(parts, strAny(x["text"], ""))
				case "tool_use":
					b, _ := json.Marshal(x["input"])
					parts = append(parts, "<tool_calls><tool_call><tool_name>"+strAny(x["name"], "")+"</tool_name><parameters>"+string(b)+"</parameters></tool_call></tool_calls>")
				case "tool_result":
					parts = append(parts, "Tool result "+strAny(x["tool_use_id"], "")+": "+strAny(x["content"], ""))
				case "input_file", "file":
					if text, err := extractInputFileText(x); err == nil {
						parts = append(parts, text)
					}
				}
			}
		}
		return strings.Join(parts, "")
	default:
		return strAny(content, "")
	}
}

func messagesFromBody(b map[string]any) []map[string]any {
	out := []map[string]any{}
	if raw, ok := b["messages"].([]any); ok {
		for _, item := range raw {
			if m, ok := item.(map[string]any); ok {
				out = append(out, m)
			}
		}
	}
	if len(out) == 0 {
		if p := strings.TrimSpace(extractPrompt(b)); p != "" {
			out = append(out, map[string]any{"role": "user", "content": p})
		}
	}
	return out
}
