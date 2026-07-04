package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	http "github.com/bogdanfinn/fhttp"
)

const codexOAuthClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
const codexOAuthTokenURL = "https://auth.openai.com/oauth/token"

func (s *Server) refreshOAuthAccount(ctx context.Context, oldToken string) (Account, error) {
	accounts := s.store.LoadAccounts()
	idx := -1
	var account Account
	for i, a := range accounts {
		if a.AccessToken == oldToken {
			idx = i
			account = a
			break
		}
	}
	if idx < 0 || account.RefreshToken == nil || strings.TrimSpace(*account.RefreshToken) == "" {
		return Account{}, fmt.Errorf("refresh_token not found")
	}
	oldRefreshToken := strings.TrimSpace(*account.RefreshToken)
	client, err := NewUpstreamClient("", s.cfg.Proxy, s.ensureCurlImpersonateBinary)
	if err != nil {
		return Account{}, err
	}
	clientID := codexOAuthClientID
	if account.ClientID != nil && strings.TrimSpace(*account.ClientID) != "" {
		clientID = strings.TrimSpace(*account.ClientID)
	}
	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", strings.TrimSpace(*account.RefreshToken))
	form.Set("scope", "openid profile email")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, codexOAuthTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return Account{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := client.client.Do(req)
	if err != nil {
		return Account{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return Account{}, fmt.Errorf("oauth refresh failed: status=%d", resp.StatusCode)
	}
	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return Account{}, err
	}
	newToken := strings.TrimSpace(strAny(payload["access_token"], ""))
	if newToken == "" {
		return Account{}, fmt.Errorf("oauth refresh response missing access_token")
	}
	refreshToken := strings.TrimSpace(strAny(payload["refresh_token"], ""))
	if refreshToken == "" && account.RefreshToken != nil {
		refreshToken = *account.RefreshToken
	}
	idToken := strings.TrimSpace(strAny(payload["id_token"], ""))
	expiresIn := intAny(payload["expires_in"], 0)
	account.AccessToken = newToken
	account.RefreshToken = &refreshToken
	if idToken != "" {
		account.IDToken = &idToken
	}
	account.SourceType = "codex"
	account.ClientID = &clientID
	if expiresIn > 0 {
		account.ExpiresAt = time.Now().Unix() + int64(expiresIn)
	}
	if account.AccountID == nil || *account.AccountID == "" {
		if accountID := chatGPTAccountID(newToken); accountID != "" {
			account.AccountID = &accountID
		}
	}
	_ = s.store.UpdateAccounts(func(accounts []Account) []Account {
		for i, a := range accounts {
			if a.AccessToken == oldToken || sameAccountID(a, account) || sameRefreshToken(a, oldRefreshToken) {
				accounts[i] = account
				return accounts
			}
		}
		accounts = append(accounts, account)
		return accounts
	})
	return account, nil
}

func sameAccountID(a Account, b Account) bool {
	if a.AccountID == nil || b.AccountID == nil {
		return false
	}
	left := strings.TrimSpace(*a.AccountID)
	right := strings.TrimSpace(*b.AccountID)
	return left != "" && left == right
}

func sameRefreshToken(a Account, refreshToken string) bool {
	refreshToken = strings.TrimSpace(refreshToken)
	return refreshToken != "" && a.RefreshToken != nil && strings.TrimSpace(*a.RefreshToken) == refreshToken
}

func (s *Server) accountByOAuthIdentity(account Account) Account {
	accounts := s.store.LoadAccounts()
	refreshToken := ""
	if account.RefreshToken != nil {
		refreshToken = strings.TrimSpace(*account.RefreshToken)
	}
	for _, item := range accounts {
		if sameAccountID(item, account) {
			return item
		}
	}
	for _, item := range accounts {
		if sameRefreshToken(item, refreshToken) {
			return item
		}
	}
	for _, item := range accounts {
		if item.AccessToken == account.AccessToken {
			return item
		}
	}
	return Account{}
}

func (s *Server) waitForOAuthRefreshResult(ctx context.Context, account Account, timeout time.Duration) (Account, bool) {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		reread := s.accountByOAuthIdentity(account)
		if reread.AccessToken != "" && reread.AccessToken != account.AccessToken {
			return reread, true
		}
		select {
		case <-waitCtx.Done():
			return Account{}, false
		case <-ticker.C:
		}
	}
}

func (s *Server) refreshOAuthAccessToken(ctx context.Context, oldToken string) (string, error) {
	account, err := s.refreshOAuthAccount(ctx, oldToken)
	if err != nil {
		return "", err
	}
	return account.AccessToken, nil
}

func (s *Server) upstreamClientForTokenWithRefresh(ctx context.Context, token string) (*UpstreamClient, error) {
	client, err := NewUpstreamClientForAccount(s.accountByToken(token), s.cfg.Proxy, s.ensureCurlImpersonateBinary)
	if err == nil {
		return client, nil
	}
	newToken, refreshErr := s.refreshOAuthAccessToken(ctx, token)
	if refreshErr != nil {
		return nil, err
	}
	return NewUpstreamClientForAccount(s.accountByToken(newToken), s.cfg.Proxy, s.ensureCurlImpersonateBinary)
}
