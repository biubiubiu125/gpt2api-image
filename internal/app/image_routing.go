package app

import (
	"strconv"
	"strings"
	"time"
)

const publicImageModel = "gpt-image-2"

const (
	imageRouteWeb   = "web"
	imageRouteCodex = "codex"
)

func normalizeImageModel(model string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	switch model {
	case "", "auto", "gpt-image", "gpt-image-1", "gpt-image-2", "dall-e-2", "dall-e-3":
		return publicImageModel
	default:
		if strings.Contains(model, "codex-gpt-image-2") {
			return publicImageModel
		}
		return publicImageModel
	}
}

func isImageModelAlias(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	switch model {
	case "gpt-image", "gpt-image-1", "gpt-image-2", "dall-e-2", "dall-e-3", "codex-gpt-image-2":
		return true
	default:
		return strings.HasSuffix(model, "-codex-gpt-image-2")
	}
}

func normalizeImageRouteStrategy(strategy string) string {
	switch strings.ToLower(strings.TrimSpace(strategy)) {
	case "", "auto", "web_first", "web-first", "webfirst":
		return "web_first"
	case "web_only", "web-only", "webonly", "web":
		return "web_only"
	case "codex_first", "codex-first", "codexfirst":
		return "codex_first"
	case "codex_only", "codex-only", "codexonly", "codex":
		return "codex_only"
	default:
		return "web_first"
	}
}

func (s *Server) imageRoutePlan() []string {
	switch normalizeImageRouteStrategy(s.cfg.ImageRouteStrategy) {
	case "web_only":
		return []string{imageRouteWeb}
	case "codex_first":
		return []string{imageRouteCodex, imageRouteWeb}
	case "codex_only":
		return []string{imageRouteCodex}
	default:
		return []string{imageRouteWeb, imageRouteCodex}
	}
}

func identityCanUseCodexImageRoute(id *Identity) bool {
	return id == nil || isRootIdentity(id) || id.CanUsePaidImageAccounts
}

func (s *Server) checkImageRouteAccess(id *Identity) error {
	if identityCanUseCodexImageRoute(id) {
		return nil
	}
	switch normalizeImageRouteStrategy(s.cfg.ImageRouteStrategy) {
	case "codex_first", "codex_only":
		return httpStatusError(403, "当前密钥无权使用付费图片账号")
	default:
		return nil
	}
}

func (s *Server) imageRoutePlanForIdentity(id *Identity) ([]string, error) {
	if err := s.checkImageRouteAccess(id); err != nil {
		return nil, err
	}
	routes := s.imageRoutePlan()
	if identityCanUseCodexImageRoute(id) {
		return routes, nil
	}
	filtered := make([]string, 0, len(routes))
	for _, route := range routes {
		if route != imageRouteCodex {
			filtered = append(filtered, route)
		}
	}
	if len(filtered) == 0 {
		return nil, httpStatusError(403, "当前密钥无权使用付费图片账号")
	}
	return filtered, nil
}

func imageRequestUsesHighResolution(size, resolution string) bool {
	switch normalizeResolution(resolution) {
	case "2k", "4k":
		return true
	case "1k":
		return false
	}
	if imageDimensionUsesHighResolution(normalizeImageDimensionSize(resolution)) {
		return true
	}
	switch normalizeImageResolution(size) {
	case "2k", "4k":
		return true
	case "1k":
		return false
	}
	return imageDimensionUsesHighResolution(normalizeImageDimensionSize(size))
}

func imageDimensionUsesHighResolution(direct string) bool {
	if direct == "" {
		return false
	}
	parts := strings.SplitN(direct, "x", 2)
	if len(parts) != 2 {
		return false
	}
	width, widthErr := strconv.Atoi(parts[0])
	height, heightErr := strconv.Atoi(parts[1])
	if widthErr != nil || heightErr != nil {
		return false
	}
	return width > 1536 || height > 1536
}

func internalImageModelForRoute(model, route string) string {
	if route == imageRouteCodex {
		return "codex-gpt-image-2"
	}
	return normalizeImageModel(model)
}

func shouldTryNextImageRoute(err error, ctxErr error) bool {
	if err == nil || ctxErr != nil {
		return false
	}
	text := strings.ToLower(err.Error())
	if strings.Contains(text, "no available image quota") || strings.Contains(text, "no available image upload quota") || strings.Contains(text, "no available codex") {
		return true
	}
	return shouldRetryImageAccount(err)
}

func isNoAvailableCodexAccountError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "no available codex")
}

func preferPreviousImageRouteError(previous, current error) error {
	if previous != nil && isNoAvailableCodexAccountError(current) {
		return previous
	}
	return current
}

func (s *Server) hasPotentialCodexImageAccount() bool {
	if s == nil || s.store == nil {
		return false
	}
	now := time.Now().UTC()
	for _, account := range s.store.LoadAccounts() {
		if isPotentialCodexImageAccount(account, now) {
			return true
		}
	}
	return false
}

func isPotentialCodexImageAccount(account Account, now time.Time) bool {
	if strings.TrimSpace(account.AccessToken) == "" || account.PendingDelete || isAccountDisabled(account.Status) || isAccountInvalidStatus(account.Status) {
		return false
	}
	if account.RefreshToken == nil || strings.TrimSpace(*account.RefreshToken) == "" {
		return false
	}
	if accountHasActiveLimitWindow(account, now, true) {
		return false
	}
	if account.ImageQuotaUnknown || account.Quota <= 0 {
		return false
	}
	if strings.ToLower(strings.TrimSpace(account.SourceType)) != "codex" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(account.Type)) {
	case "plus", "team", "pro":
		return true
	default:
		return false
	}
}
