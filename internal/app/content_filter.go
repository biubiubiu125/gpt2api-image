package app

import (
	"fmt"
	"strings"
)

func (s *Server) checkContent(text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	lower := strings.ToLower(text)
	for _, word := range s.cfg.SensitiveWords {
		w := strings.ToLower(strings.TrimSpace(word))
		if w != "" && strings.Contains(lower, w) {
			return fmt.Errorf("请求包含敏感词：%s", word)
		}
	}
	return nil
}
