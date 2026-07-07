package app

import (
	"testing"
	"time"
)

func TestAccountNeedsLimitRefreshIncludesUnknownUploadQuota(t *testing.T) {
	if accountNeedsLimitRefresh(Account{AccessToken: "tok", Status: accountStatusNormal, UploadQuotaUnknown: true}) != true {
		t.Fatal("unknown upload quota should be refreshed")
	}
}

func TestAccountNeedsLimitRefreshIncludesUnavailableImageQuotaWithUploadQuota(t *testing.T) {
	if !accountNeedsLimitRefresh(Account{AccessToken: "tok", Status: accountStatusNormal, ImageQuotaUnknown: true, UploadQuota: 2}) {
		t.Fatal("image-unknown account with upload quota should be refreshed")
	}
	if !accountNeedsLimitRefresh(Account{AccessToken: "tok", Status: accountStatusNormal, Quota: 0, UploadQuota: 2}) {
		t.Fatal("image-empty account with upload quota should be refreshed")
	}
}

func TestAccountNeedsLimitRefreshSkipsLocalUnusableAccounts(t *testing.T) {
	cases := []Account{
		{AccessToken: "disabled", Status: accountStatusDisabled, UploadQuotaUnknown: true},
		{AccessToken: "invalid", Status: accountStatusInvalid, UploadQuotaUnknown: true},
		{AccessToken: "pending", Status: accountStatusNormal, UploadQuotaUnknown: true, PendingDelete: true},
	}
	for _, tc := range cases {
		if accountNeedsLimitRefresh(tc) {
			t.Fatalf("account %s should not be refreshed", tc.AccessToken)
		}
	}
}

func TestAccountsNeedingLimitRefreshFiltersUnusableAccounts(t *testing.T) {
	future := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	got := accountsNeedingLimitRefresh([]Account{
		{AccessToken: "unknown", Status: accountStatusNormal, UploadQuotaUnknown: true},
		{AccessToken: "limited", Status: accountStatusLimited, UploadQuota: 0},
		{AccessToken: "available", Status: accountStatusNormal, Quota: 25, UploadQuota: 1},
		{AccessToken: "waiting", Status: accountStatusNormal, Quota: 25, UploadQuota: 0, UploadQuotaUnknown: false, UploadLimitResetAt: &future},
		{AccessToken: "disabled", Status: accountStatusDisabled, UploadQuotaUnknown: true},
		{AccessToken: "invalid", Status: accountStatusInvalid, UploadQuotaUnknown: true},
		{AccessToken: "pending", Status: accountStatusNormal, UploadQuotaUnknown: true, PendingDelete: true},
	})
	if len(got) != 2 || got[0] != "unknown" || got[1] != "limited" {
		t.Fatalf("refresh tokens = %#v, want unknown and limited", got)
	}
}

func TestAccountNeedsLimitRefreshSkipsKnownAvailableUploadQuota(t *testing.T) {
	if accountNeedsLimitRefresh(Account{AccessToken: "tok", Status: accountStatusNormal, Quota: 25, UploadQuota: 1}) {
		t.Fatal("known available upload quota should not be refreshed")
	}
}

func TestAccountNeedsLimitRefreshKnownExhaustedUploadQuotaResetWindow(t *testing.T) {
	future := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	past := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)

	if accountNeedsLimitRefresh(Account{AccessToken: "tok", Status: accountStatusNormal, Quota: 25, UploadQuota: 0, UploadQuotaUnknown: false, UploadLimitResetAt: &future}) {
		t.Fatal("known exhausted upload quota should wait until reset time")
	}
	if !accountNeedsLimitRefresh(Account{AccessToken: "tok", Status: accountStatusNormal, Quota: 25, UploadQuota: 0, UploadQuotaUnknown: false, UploadLimitResetAt: &past}) {
		t.Fatal("known exhausted upload quota should refresh after reset time")
	}
}
