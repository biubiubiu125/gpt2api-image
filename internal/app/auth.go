package app

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

func normalizeKeys(items []UserKey) []UserKey {
	out := make([]UserKey, 0, len(items))
	for _, k := range items {
		out = append(out, normalizeServiceKey(k))
	}
	return out
}

func normalizeServiceKey(k UserKey) UserKey {
	if k.ID == "" {
		k.ID = randID(6)
	}
	if k.Name == "" {
		k.Name = "API 密钥"
	}
	if k.KeyHash == "" && k.Key != "" {
		k.KeyHash = hashKey(k.Key)
	}
	if k.CreatedAt == "" {
		k.CreatedAt = nowISO()
	}
	if k.ImageDailyResetAt == "" {
		k.ImageDailyResetAt = todayKey()
	}
	if k.ImageMonthlyResetAt == "" {
		k.ImageMonthlyResetAt = monthKey()
	}
	if k.ChatDailyResetAt == "" {
		k.ChatDailyResetAt = todayKey()
	}
	if k.ChatMonthlyResetAt == "" {
		k.ChatMonthlyResetAt = monthKey()
	}
	k.Role = "admin"
	k.AccountTier = "premium"
	k.ImageDailyQuota = 0
	k.ImageMonthlyQuota = 0
	k.ImageTotalQuota = 0
	k.ChatDailyQuota = 0
	k.ChatMonthlyQuota = 0
	k.ChatTotalQuota = 0
	k.ImageDailyUnlimited = true
	k.ImageMonthlyUnlimited = true
	k.ImageTotalUnlimited = true
	k.ChatDailyUnlimited = true
	k.ChatMonthlyUnlimited = true
	k.ChatTotalUnlimited = true
	return k
}

func (s *Server) bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	parts := strings.SplitN(h, " ", 2)
	if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
		return strings.TrimSpace(parts[1])
	}
	return ""
}

func (s *Server) requireIdentity(w http.ResponseWriter, r *http.Request) (*Identity, bool) {
	token := s.bearer(r)
	if token == "" {
		writeErr(w, 401, "密钥无效或已失效，请重新登录")
		return nil, false
	}
	if token == s.cfg.AuthKey {
		return &Identity{ID: "admin", Name: "管理员", Role: "admin", AccountTier: "premium", CanUsePaidImageAccounts: true, CanUseHighResolution: true}, true
	}
	keys := s.store.LoadAuthKeys()
	h := hashKey(token)
	for _, k := range keys {
		if !k.Enabled {
			continue
		}
		if k.KeyHash != "" && subtle.ConstantTimeCompare([]byte(k.KeyHash), []byte(h)) == 1 {
			now := nowISO()
			keyID := k.ID
			_ = s.store.UpdateAuthKeys(func(keys []UserKey) []UserKey {
				for i := range keys {
					if keys[i].ID == keyID {
						keys[i].LastUsedAt = &now
						break
					}
				}
				return keys
			})
			return &Identity{ID: k.ID, Name: k.Name, Role: "admin", AccountTier: "premium", CanUsePaidImageAccounts: true, CanUseHighResolution: true}, true
		}
	}
	writeErr(w, 401, "密钥无效或已失效，请重新登录")
	return nil, false
}

func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request) (*Identity, bool) {
	id, ok := s.requireIdentity(w, r)
	if !ok {
		return nil, false
	}
	if id.Role != "admin" {
		writeErr(w, 403, "需要管理员权限才能执行这个操作")
		return nil, false
	}
	return id, true
}

func (s *Server) checkImageAccess(id *Identity, model, resolution string) error {
	if id == nil || id.Role == "admin" {
		return nil
	}
	res := normalizeResolution(resolution)
	if (res == "2k" || res == "4k") && !id.CanUseHighResolution {
		return httpStatusError(403, "当前密钥无权使用 2K/4K 图片分辨率")
	}
	if isCodexImageRequest(model, resolution) && !id.CanUsePaidImageAccounts {
		return httpStatusError(403, "当前密钥无权使用付费图片账号")
	}
	return nil
}

func publicKey(k UserKey) map[string]any {
	k = normalizeServiceKey(k)
	res := map[string]any{"id": k.ID, "name": k.Name, "role": "admin", "enabled": k.Enabled, "created_at": k.CreatedAt, "last_used_at": k.LastUsedAt, "account_tier": "premium", "can_use_paid_image_accounts": true, "can_use_high_resolution": true, "key_visible": k.Key != ""}
	add := func(prefix string, quota, used int, unl bool) {
		res[prefix+"_quota"] = quota
		res[prefix+"_used"] = used
		res[prefix+"_unlimited"] = unl
		if unl {
			res[prefix+"_remaining"] = nil
		} else {
			rem := quota - used
			if rem < 0 {
				rem = 0
			}
			res[prefix+"_remaining"] = rem
		}
	}
	add("image_daily", k.ImageDailyQuota, k.ImageDailyUsed, k.ImageDailyUnlimited)
	add("image_monthly", k.ImageMonthlyQuota, k.ImageMonthlyUsed, k.ImageMonthlyUnlimited)
	add("image_total", k.ImageTotalQuota, k.ImageTotalUsed, k.ImageTotalUnlimited)
	add("chat_daily", k.ChatDailyQuota, k.ChatDailyUsed, k.ChatDailyUnlimited)
	add("chat_monthly", k.ChatMonthlyQuota, k.ChatMonthlyUsed, k.ChatMonthlyUnlimited)
	add("chat_total", k.ChatTotalQuota, k.ChatTotalUsed, k.ChatTotalUnlimited)
	return res
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	id, ok := s.requireIdentity(w, r)
	if !ok {
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "version": s.version(), "role": id.Role, "subject_id": id.ID, "name": id.Name, "account_tier": id.AccountTier, "can_use_paid_image_accounts": id.CanUsePaidImageAccounts, "can_use_high_resolution": id.CanUseHighResolution})
}

func (s *Server) handleAuthMe(w http.ResponseWriter, r *http.Request) {
	id, ok := s.requireIdentity(w, r)
	if !ok {
		return
	}
	if id.Role == "admin" {
		payload := map[string]any{"id": id.ID, "name": id.Name, "role": "admin", "account_tier": "premium", "can_use_paid_image_accounts": true, "can_use_high_resolution": true}
		for _, k := range []string{"image_daily", "image_monthly", "image_total", "chat_daily", "chat_monthly", "chat_total"} {
			payload[k+"_quota"] = 0
			payload[k+"_used"] = 0
			payload[k+"_unlimited"] = true
			payload[k+"_remaining"] = nil
		}
		writeJSON(w, 200, map[string]any{"identity": payload})
		return
	}
	for _, k := range s.store.LoadAuthKeys() {
		if k.ID == id.ID {
			writeJSON(w, 200, map[string]any{"identity": publicKey(k)})
			return
		}
	}
	writeErr(w, 404, "用户不存在")
}

func (s *Server) handleAuthUsers(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		items := []map[string]any{}
		for _, k := range s.store.LoadAuthKeys() {
			items = append(items, publicKey(k))
		}
		writeJSON(w, 200, map[string]any{"items": items})
	case http.MethodPost:
		var body map[string]any
		if !readBody(w, r, &body) {
			return
		}
		raw := strings.TrimSpace(strAny(body["key"], ""))
		if raw == "" {
			raw = "sk-" + randID(24)
		}
		k := normalizeServiceKey(UserKey{ID: randID(6), Name: strings.TrimSpace(strAny(body["name"], "API 密钥")), KeyHash: hashKey(raw), Key: raw, Enabled: true, CreatedAt: nowISO()})
		_ = s.store.UpdateAuthKeys(func(keys []UserKey) []UserKey {
			return append(keys, k)
		})
		writeJSON(w, 200, map[string]any{"item": publicKey(k), "key": raw, "items": s.publicUserKeys()})
	default:
		writeErr(w, 405, "method not allowed")
	}
}

func (s *Server) handleAuthUserID(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/auth/users/")
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if parts[0] == "" {
		writeErr(w, 404, "not found")
		return
	}
	id := parts[0]
	keys := s.store.LoadAuthKeys()
	idx := -1
	for i, k := range keys {
		if k.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		writeErr(w, 404, "这条 API 密钥不存在，可能已经被删除")
		return
	}
	if len(parts) > 1 && parts[1] == "key" {
		writeJSON(w, 200, map[string]any{"key": keys[idx].Key, "key_visible": keys[idx].Key != ""})
		return
	}
	if len(parts) > 1 && parts[1] == "regenerate" {
		raw := "sk-" + randID(24)
		if r.Method == http.MethodPost {
			var b map[string]any
			if !readBody(w, r, &b) {
				return
			}
			if v := strings.TrimSpace(strAny(b["key"], "")); v != "" {
				raw = v
			}
		}
		var item UserKey
		_ = s.store.UpdateAuthKeys(func(keys []UserKey) []UserKey {
			for i := range keys {
				if keys[i].ID == id {
					keys[i].Key = raw
					keys[i].KeyHash = hashKey(raw)
					item = normalizeServiceKey(keys[i])
					keys[i] = item
					break
				}
			}
			return keys
		})
		writeJSON(w, 200, map[string]any{"item": publicKey(item), "key": raw, "items": s.publicUserKeys()})
		return
	}
	switch r.Method {
	case http.MethodDelete:
		_ = s.store.UpdateAuthKeys(func(keys []UserKey) []UserKey {
			out := keys[:0]
			for _, k := range keys {
				if k.ID == id {
					continue
				}
				out = append(out, k)
			}
			return out
		})
		writeJSON(w, 200, map[string]any{"items": s.publicUserKeys()})
	case http.MethodPost:
		var b map[string]any
		if !readBody(w, r, &b) {
			return
		}
		var k UserKey
		_ = s.store.UpdateAuthKeys(func(keys []UserKey) []UserKey {
			for i := range keys {
				if keys[i].ID != id {
					continue
				}
				k = keys[i]
				if v, ok := b["name"]; ok {
					k.Name = strAny(v, k.Name)
				}
				if v, ok := b["enabled"]; ok {
					k.Enabled = boolAny(v, k.Enabled)
				}
				if v := strings.TrimSpace(strAny(b["key"], "")); v != "" {
					k.Key = v
					k.KeyHash = hashKey(v)
				}
				k = normalizeServiceKey(k)
				keys[i] = k
				break
			}
			return keys
		})
		writeJSON(w, 200, map[string]any{"item": publicKey(k), "items": s.publicUserKeys()})
	default:
		writeErr(w, 405, "method not allowed")
	}
}

func (s *Server) publicUserKeys() []map[string]any {
	out := []map[string]any{}
	for _, k := range s.store.LoadAuthKeys() {
		out = append(out, publicKey(k))
	}
	return out
}
func applyQuotaUpdate(k *UserKey, b map[string]any) {
	if v, ok := b["image_daily_quota"]; ok {
		k.ImageDailyQuota = intAny(v, k.ImageDailyQuota)
	}
	if v, ok := b["image_daily_unlimited"]; ok {
		k.ImageDailyUnlimited = boolAny(v, k.ImageDailyUnlimited)
	}
	if v, ok := b["image_monthly_quota"]; ok {
		k.ImageMonthlyQuota = intAny(v, k.ImageMonthlyQuota)
	}
	if v, ok := b["image_monthly_unlimited"]; ok {
		k.ImageMonthlyUnlimited = boolAny(v, k.ImageMonthlyUnlimited)
	}
	if v, ok := b["image_total_quota"]; ok {
		k.ImageTotalQuota = intAny(v, k.ImageTotalQuota)
	}
	if v, ok := b["image_total_unlimited"]; ok {
		k.ImageTotalUnlimited = boolAny(v, k.ImageTotalUnlimited)
	}
	if v, ok := b["chat_daily_quota"]; ok {
		k.ChatDailyQuota = intAny(v, k.ChatDailyQuota)
	}
	if v, ok := b["chat_daily_unlimited"]; ok {
		k.ChatDailyUnlimited = boolAny(v, k.ChatDailyUnlimited)
	}
	if v, ok := b["chat_monthly_quota"]; ok {
		k.ChatMonthlyQuota = intAny(v, k.ChatMonthlyQuota)
	}
	if v, ok := b["chat_monthly_unlimited"]; ok {
		k.ChatMonthlyUnlimited = boolAny(v, k.ChatMonthlyUnlimited)
	}
	if v, ok := b["chat_total_quota"]; ok {
		k.ChatTotalQuota = intAny(v, k.ChatTotalQuota)
	}
	if v, ok := b["chat_total_unlimited"]; ok {
		k.ChatTotalUnlimited = boolAny(v, k.ChatTotalUnlimited)
	}
	if boolAny(b["reset_image_daily_used"], false) {
		k.ImageDailyUsed = 0
		k.ImageDailyResetAt = todayKey()
	}
	if boolAny(b["reset_image_monthly_used"], false) {
		k.ImageMonthlyUsed = 0
		k.ImageMonthlyResetAt = monthKey()
	}
	if boolAny(b["reset_image_total_used"], false) {
		k.ImageTotalUsed = 0
	}
	if boolAny(b["reset_chat_daily_used"], false) {
		k.ChatDailyUsed = 0
		k.ChatDailyResetAt = todayKey()
	}
	if boolAny(b["reset_chat_monthly_used"], false) {
		k.ChatMonthlyUsed = 0
		k.ChatMonthlyResetAt = monthKey()
	}
	if boolAny(b["reset_chat_total_used"], false) {
		k.ChatTotalUsed = 0
	}
}

func (s *Server) consumeImage(id *Identity, n int) bool {
	if id.Role == "admin" {
		return true
	}
	return s.consumeQuota(id.ID, n, true)
}
func (s *Server) consumeChat(id *Identity, n int) bool {
	if id.Role == "admin" {
		return true
	}
	return s.consumeQuota(id.ID, n, false)
}
func (s *Server) consumeQuota(id string, n int, image bool) bool {
	if n < 1 {
		n = 1
	}
	consumed := false
	_ = s.store.UpdateAuthKeys(func(keys []UserKey) []UserKey {
		for i, k := range keys {
			if k.ID != id {
				continue
			}
			resetPeriods(&k)
			if image {
				if !enough(k.ImageDailyQuota, k.ImageDailyUsed, k.ImageDailyUnlimited, n) || !enough(k.ImageMonthlyQuota, k.ImageMonthlyUsed, k.ImageMonthlyUnlimited, n) || !enough(k.ImageTotalQuota, k.ImageTotalUsed, k.ImageTotalUnlimited, n) {
					return keys
				}
				k.ImageDailyUsed += n
				k.ImageMonthlyUsed += n
				k.ImageTotalUsed += n
			} else {
				if !enough(k.ChatDailyQuota, k.ChatDailyUsed, k.ChatDailyUnlimited, n) || !enough(k.ChatMonthlyQuota, k.ChatMonthlyUsed, k.ChatMonthlyUnlimited, n) || !enough(k.ChatTotalQuota, k.ChatTotalUsed, k.ChatTotalUnlimited, n) {
					return keys
				}
				k.ChatDailyUsed += n
				k.ChatMonthlyUsed += n
				k.ChatTotalUsed += n
			}
			keys[i] = k
			consumed = true
			return keys
		}
		return keys
	})
	return consumed
}
func (s *Server) refundImage(id *Identity, n int) {
	if id == nil || id.Role == "admin" || n <= 0 {
		return
	}
	s.refundQuota(id.ID, n, true)
}
func (s *Server) refundChat(id *Identity, n int) {
	if id == nil || id.Role == "admin" || n <= 0 {
		return
	}
	s.refundQuota(id.ID, n, false)
}
func (s *Server) refundQuota(id string, n int, image bool) {
	_ = s.store.UpdateAuthKeys(func(keys []UserKey) []UserKey {
		for i, k := range keys {
			if k.ID != id {
				continue
			}
			if image {
				k.ImageDailyUsed = maxInt(0, k.ImageDailyUsed-n)
				k.ImageMonthlyUsed = maxInt(0, k.ImageMonthlyUsed-n)
				k.ImageTotalUsed = maxInt(0, k.ImageTotalUsed-n)
			} else {
				k.ChatDailyUsed = maxInt(0, k.ChatDailyUsed-n)
				k.ChatMonthlyUsed = maxInt(0, k.ChatMonthlyUsed-n)
				k.ChatTotalUsed = maxInt(0, k.ChatTotalUsed-n)
			}
			keys[i] = k
			return keys
		}
		return keys
	})
}
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
func enough(q, u int, unl bool, n int) bool { return unl || q-u >= n }
func resetPeriods(k *UserKey) {
	if k.ImageDailyResetAt != todayKey() {
		k.ImageDailyUsed = 0
		k.ImageDailyResetAt = todayKey()
	}
	if k.ChatDailyResetAt != todayKey() {
		k.ChatDailyUsed = 0
		k.ChatDailyResetAt = todayKey()
	}
	if k.ImageMonthlyResetAt != monthKey() {
		k.ImageMonthlyUsed = 0
		k.ImageMonthlyResetAt = monthKey()
	}
	if k.ChatMonthlyResetAt != monthKey() {
		k.ChatMonthlyUsed = 0
		k.ChatMonthlyResetAt = monthKey()
	}
}
