package app

import "strings"

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
	if strings.Contains(text, "no available image quota") || strings.Contains(text, "no available codex") {
		return true
	}
	return shouldRetryImageAccount(err)
}
