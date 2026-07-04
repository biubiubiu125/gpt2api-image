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
		if !isSupportedImageModel(id) {
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
	accounts := s.store.LoadAccounts()
	hasWeb := false
	codexTypes := map[string]bool{}
	for _, a := range accounts {
		if a.AccessToken == "" {
			continue
		}
		if strings.ToLower(a.SourceType) == "codex" {
			codexTypes[strings.ToLower(a.Type)] = true
		} else {
			hasWeb = true
		}
	}
	ids := []string{}
	add := func(id string) {
		for _, existing := range ids {
			if existing == id {
				return
			}
		}
		ids = append(ids, id)
	}
	if hasWeb || len(accounts) == 0 {
		add("gpt-image-2")
	}
	if codexTypes["plus"] || codexTypes["team"] || codexTypes["pro"] {
		add("codex-gpt-image-2")
	}
	if codexTypes["plus"] {
		add("plus-codex-gpt-image-2")
	}
	if codexTypes["team"] {
		add("team-codex-gpt-image-2")
	}
	if codexTypes["pro"] {
		add("pro-codex-gpt-image-2")
	}
	if len(ids) == 0 {
		add("gpt-image-2")
	}
	return ids
}

func modelListItem(id string, created int64) map[string]any {
	return map[string]any{"id": id, "object": "model", "created": created, "owned_by": "gpt2api-image", "permission": []any{}, "root": id, "parent": nil}
}
