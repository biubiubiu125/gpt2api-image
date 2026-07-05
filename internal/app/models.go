package app

import "strings"

func (s *Server) mergeDynamicModels(result map[string]any) map[string]any {
	data, _ := result["data"].([]map[string]any)
	if data == nil {
		if raw, ok := result["data"].([]any); ok {
			for _, item := range raw {
				if m, ok := item.(map[string]any); ok {
					data = append(data, m)
				}
			}
		}
	}
	filtered := []map[string]any{}
	seen := map[string]bool{}
	for _, item := range data {
		id := strings.TrimSpace(strAny(item["id"], ""))
		if !strings.EqualFold(id, publicImageModel) {
			continue
		}
		seen[id] = true
		filtered = append(filtered, item)
	}
	data = filtered
	for _, id := range s.imageModelIDs() {
		if id != "" && !seen[id] {
			seen[id] = true
			data = append(data, modelListItem(id, 0))
		}
	}
	result["data"] = data
	return result
}

func (s *Server) imageModelIDs() []string {
	return []string{publicImageModel}
}

func modelListItem(id string, created int64) map[string]any {
	return map[string]any{"id": id, "object": "model", "created": created, "owned_by": "gpt2api-image", "permission": []any{}, "root": id, "parent": nil}
}
