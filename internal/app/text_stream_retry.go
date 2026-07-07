package app

import (
	"context"
	"errors"
	"time"
)

type upstreamStreamAttempt struct {
	Client *UpstreamClient
	Events <-chan UpstreamTextEvent
	Errs   <-chan error
}

func (s *Server) streamTextWithRetry(ctx context.Context, messages []map[string]any, model, conversationID, planType string) (<-chan UpstreamTextEvent, <-chan error) {
	return s.streamTextWithRetryMode(ctx, messages, model, conversationID, planType, true)
}

func (s *Server) streamChatWithRetry(ctx context.Context, messages []map[string]any, model, conversationID, planType string) (<-chan UpstreamTextEvent, <-chan error) {
	return s.streamTextWithRetryMode(ctx, messages, model, conversationID, planType, false)
}

func (s *Server) streamTextWithRetryMode(ctx context.Context, messages []map[string]any, model, conversationID, planType string, historyDisabled bool) (<-chan UpstreamTextEvent, <-chan error) {
	out := make(chan UpstreamTextEvent)
	errs := make(chan error, 1)
	go func() {
		defer close(out)
		defer close(errs)
		excluded := map[string]bool{}
		requiredUploads := countUploadImagesInMessages(messages)
		attempt := 0
		for {
			attempt++
			traceLogf(ctx, "text account attempt %d plan_type=%s excluded=%d uploads=%d", attempt, planType, len(excluded), requiredUploads)
			account, err := s.pickTextAccountExcludingForUploads(excluded, planType, requiredUploads)
			if err != nil {
				errs <- err
				return
			}
			token := account.AccessToken
			traceLogf(ctx, "selected text account %s", accountLabel(account))
			excluded[token] = true
			leaseID := ""
			if requiredUploads > 0 {
				var leased bool
				leaseID, leased, err = s.acquireUploadAccountLease(ctx, token, account, requiredUploads, "text-upload-"+randID(8), 7*time.Minute)
				if err != nil {
					s.releaseTextUploadSelection(ctx, token, leaseID, requiredUploads)
					if errors.Is(err, errImageUploadQuotaReserved) {
						continue
					}
					errs <- err
					return
				}
				if !leased {
					s.releaseTextUploadSelection(ctx, token, leaseID, requiredUploads)
					continue
				}
			}
			client, err := NewUpstreamClientForAccount(account, s.cfg.Proxy, s.ensureCurlImpersonateBinary)
			if err != nil {
				s.releaseTextUploadSelection(ctx, token, leaseID, requiredUploads)
				errs <- err
				return
			}
			if requiredUploads > 0 {
				client.onUploadSuccess = func() {
					s.markAccountUploadSuccess(token, 1)
				}
			}
			var events <-chan UpstreamTextEvent
			var streamErrs <-chan error
			if historyDisabled {
				events, streamErrs = client.StreamTextConversationEvents(ctx, messages, model, conversationID)
			} else {
				events, streamErrs = client.StreamChatConversationEvents(ctx, messages, model, conversationID)
			}
			emitted := false
			for ev := range events {
				ev.AccountToken = token
				if ev.Delta != "" {
					emitted = true
				}
				out <- ev
			}
			err = <-streamErrs
			s.releaseTextUploadSelection(ctx, token, leaseID, requiredUploads)
			if err == nil {
				traceLogf(ctx, "text account attempt %d success", attempt)
				s.markAccountSuccess(token, false)
				errs <- nil
				return
			}
			traceLogf(ctx, "text account attempt %d failed emitted=%v error=%v", attempt, emitted, err)
			s.markAccountFailure(token, err, false)
			if !emitted && (isUploadLimitErrorText(err) || isInvalidTokenErrorText(err) || isUpstreamBlockErrorText(err) || isTurnstileRequirementErrorText(err) || isRetryableBootstrapError(err)) {
				traceLogf(ctx, "retry with another text account")
				continue
			}
			errs <- err
			return
		}
	}()
	return out, errs
}

func countUploadImagesInMessages(messages []map[string]any) int {
	total := 0
	for _, message := range messages {
		_, rawContent := upstreamConversationRoleAndContent(message)
		total += len(extractImagesFromContent(rawContent))
	}
	return total
}

func (s *Server) collectTextWithRetry(ctx context.Context, messages []map[string]any, model string) (string, error) {
	events, errs := s.streamTextWithRetry(ctx, messages, model, "", "")
	text := ""
	for ev := range events {
		text += ev.Delta
	}
	if err := <-errs; err != nil {
		return "", err
	}
	return text, nil
}
