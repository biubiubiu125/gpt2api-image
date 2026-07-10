"use client";

import { create } from "zustand";
import { toast } from "sonner";

import {
  fetchRegisterConfig,
  fetchSettingsConfig,
  repairAbnormalAccounts,
  resetRegister as resetRegisterApi,
  startRegister,
  stopRegister,
  testRegisterOutlookPool,
  updateRegisterConfig,
  updateSettingsConfig,
  type RegisterConfig,
  type SettingsConfig,
} from "@/lib/api";
import { getRegisterRuntimeState } from "@/lib/register-runtime";

function normalizeConfig(config: SettingsConfig): SettingsConfig {
  return {
    ...config,
    refresh_account_interval_minute: Number(config.refresh_account_interval_minute || 60),
    image_retention_days: Number(config.image_retention_days || 15),
    image_max_storage_mb: Math.max(0, Number(config.image_max_storage_mb) || 0),
    cleanup_protect_user_images: Boolean(config.cleanup_protect_user_images ?? true),
    image_poll_timeout_secs: Number(config.image_poll_timeout_secs || 120),
    image_poll_interval_secs: Number(config.image_poll_interval_secs || 4),
    image_poll_initial_wait_secs: Number(config.image_poll_initial_wait_secs || 0),
    image_account_concurrency: Number(config.image_account_concurrency || 3),
    image_account_fallback_attempts: Number(config.image_account_fallback_attempts || 3),
    auto_remove_invalid_accounts: Boolean(config.auto_remove_invalid_accounts ?? true),
    auto_remove_rate_limited_accounts: Boolean(config.auto_remove_rate_limited_accounts ?? true),
    auto_delete_quota_zero_accounts: Boolean(config.auto_delete_quota_zero_accounts ?? true),
    auto_delete_upload_quota_zero_accounts: Boolean(config.auto_delete_upload_quota_zero_accounts ?? true),
    delete_403_consecutive: Number(config.delete_403_consecutive || 2),
    delete_timeout_consecutive: Number(config.delete_timeout_consecutive || 2),
    auto_refresh_accounts_enabled: Boolean(config.auto_refresh_accounts_enabled ?? true),
    auto_refresh_accounts_interval_minutes: Number(config.auto_refresh_accounts_interval_minutes || 60),
    auto_refresh_accounts_batch_size: Number(config.auto_refresh_accounts_batch_size || 0),
    auto_refresh_delete_failed_accounts: Boolean(config.auto_refresh_delete_failed_accounts ?? true),
    auto_refresh_trigger_refill: Boolean(config.auto_refresh_trigger_refill ?? true),
    auto_cleanup_accounts_enabled: Boolean(config.auto_cleanup_accounts_enabled ?? true),
    auto_cleanup_accounts_interval_seconds: Number(config.auto_cleanup_accounts_interval_seconds || 60),
    auto_refill_use_effective_available: Boolean(config.auto_refill_use_effective_available ?? true),
    log_levels: Array.isArray(config.log_levels) ? config.log_levels : [],
    proxy: typeof config.proxy === "string" ? config.proxy : "",
    image_route_strategy: String(config.image_route_strategy || "web_first"),
    base_url: typeof config.base_url === "string" ? config.base_url : "",
    global_system_prompt: String(config.global_system_prompt || ""),
    sensitive_words: Array.isArray(config.sensitive_words) ? config.sensitive_words : [],
  };
}

function sanitizeRegisterProviders(providers: Array<Record<string, unknown>> | undefined): Array<Record<string, unknown>> {
  return (providers || []).map((provider) => {
    const nextProvider = { ...provider };
    delete nextProvider.auto_domain_blacklist;
    delete nextProvider.auto_domain_blacklist_entries;
    delete nextProvider.mailboxes_count;
    delete nextProvider.mailboxes_preview;
    delete nextProvider.mailboxes_stats;
    return nextProvider;
  });
}

function buildRegisterMailPayload(config: RegisterConfig["mail"]): RegisterConfig["mail"] {
  return {
    ...config,
    request_timeout: Number(config.request_timeout || 30),
    wait_timeout: Number(config.wait_timeout || 180),
    wait_interval: Number(config.wait_interval || 5),
    api_use_register_proxy: false,
    providers: sanitizeRegisterProviders(config.providers),
  };
}

function normalizeRegister(config: RegisterConfig): RegisterConfig {
  return {
    ...config,
    mail: {
      request_timeout: Number(config.mail?.request_timeout || 30),
      wait_timeout: Number(config.mail?.wait_timeout || 180),
      wait_interval: Number(config.mail?.wait_interval || 5),
      api_use_register_proxy: false,
      providers: Array.isArray(config.mail?.providers) ? config.mail.providers : [],
    },
    proxy: String(config.proxy || ""),
    task_timeout_seconds: Math.max(30, Number(config.task_timeout_seconds) || 300),
    task_stall_timeout_seconds: Math.max(0, Number(config.task_stall_timeout_seconds ?? 60)),
    total: Number(config.total || 1),
    threads: Number(config.threads || 1),
    mode: config.mode || "total",
    target_quota: Number(config.target_quota || 1),
    target_available: Number(config.target_available || 1),
    check_interval: Number(config.check_interval || 5),
    fixed_password: String(config.fixed_password || ""),
    auto_refill: {
      enabled: Boolean(config.auto_refill?.enabled),
      min_available: Math.max(1, Number(config.auto_refill?.min_available) || 30),
      batch_total: Math.max(1, Number(config.auto_refill?.batch_total) || 100),
      check_interval: Math.max(10, Number(config.auto_refill?.check_interval) || 300),
    },
    stats: {
      ...(config.stats || {}),
      success: Number(config.stats?.success || 0),
      usable_success: Number(config.stats?.usable_success ?? config.stats?.success ?? 0),
      fail: Number(config.stats?.fail || 0),
      done: Number(config.stats?.done || 0),
      saved: Number(config.stats?.saved || 0),
      refresh_failed: Number(config.stats?.refresh_failed || 0),
      token_acquired_refresh_failed: Number(config.stats?.token_acquired_refresh_failed || 0),
      running: Number(config.stats?.running || 0),
      threads: Number(config.stats?.threads || config.threads || 1),
      failure_reasons: config.stats?.failure_reasons || {},
      lifecycle: String(config.stats?.lifecycle || config.lifecycle || "idle"),
      is_running: Boolean(config.stats?.is_running ?? config.is_running),
      is_stopping: Boolean(config.stats?.is_stopping ?? config.is_stopping),
    },
    logs: Array.isArray(config.logs) ? config.logs : [],
  };
}

function isRegisterDraining(config: RegisterConfig | null) {
  return getRegisterRuntimeState(config).isStopping;
}

function isRegisterActive(config: RegisterConfig | null) {
  return getRegisterRuntimeState(config).isActive;
}

type SettingsStore = {
  config: SettingsConfig | null;
  isLoadingConfig: boolean;
  isSavingConfig: boolean;
  isDirty: boolean;

  registerConfig: RegisterConfig | null;
  isLoadingRegister: boolean;
  isSavingRegister: boolean;

  initialize: () => Promise<void>;
  loadConfig: () => Promise<void>;
  saveConfig: () => Promise<boolean>;
  revertConfig: () => Promise<void>;

  setRefreshAccountIntervalMinute: (value: string) => void;
  setImageRetentionDays: (value: string) => void;
  setImageMaxStorageMB: (value: string) => void;
  setCleanupProtectUserImages: (value: boolean) => void;
  setImagePollTimeoutSecs: (value: string) => void;
  setImagePollIntervalSecs: (value: string) => void;
  setImagePollInitialWaitSecs: (value: string) => void;
  setImageAccountConcurrency: (value: string) => void;
  setImageAccountFallbackAttempts: (value: string) => void;
  setAutoRemoveInvalidAccounts: (value: boolean) => void;
  setAutoRemoveRateLimitedAccounts: (value: boolean) => void;
  setAutoDeleteQuotaZeroAccounts: (value: boolean) => void;
  setAutoDeleteUploadQuotaZeroAccounts: (value: boolean) => void;
  setDelete403Consecutive: (value: string) => void;
  setDeleteTimeoutConsecutive: (value: string) => void;
  setAutoRefreshAccountsEnabled: (value: boolean) => void;
  setAutoRefreshAccountsIntervalMinutes: (value: string) => void;
  setAutoRefreshAccountsBatchSize: (value: string) => void;
  setAutoRefreshDeleteFailedAccounts: (value: boolean) => void;
  setAutoRefreshTriggerRefill: (value: boolean) => void;
  setAutoCleanupAccountsEnabled: (value: boolean) => void;
  setAutoCleanupAccountsIntervalSeconds: (value: string) => void;
  setAutoRefillUseEffectiveAvailable: (value: boolean) => void;
  setLogLevel: (level: string, enabled: boolean) => void;
  setProxy: (value: string) => void;
  setImageRouteStrategy: (value: string) => void;
  setBaseUrl: (value: string) => void;
  setGlobalSystemPrompt: (value: string) => void;
  setSensitiveWordsText: (value: string) => void;

  loadRegister: (silent?: boolean) => Promise<void>;
  setRegisterConfig: (config: RegisterConfig) => void;
  setRegisterProxy: (value: string) => void;
  setRegisterTotal: (value: string) => void;
  setRegisterThreads: (value: string) => void;
  setRegisterMode: (value: "total" | "quota" | "available") => void;
  setRegisterTargetQuota: (value: string) => void;
  setRegisterTargetAvailable: (value: string) => void;
  setRegisterCheckInterval: (value: string) => void;
  setRegisterFixedPassword: (value: string) => void;
  setRegisterTaskTimeoutSeconds: (value: string) => void;
  setRegisterTaskStallTimeoutSeconds: (value: string) => void;
  setRegisterMailField: (key: "request_timeout" | "wait_timeout" | "wait_interval", value: string) => void;
  setRegisterMailUseRegisterProxy: (value: boolean) => void;
  setRegisterAutoRefillField: (key: "enabled" | "min_available" | "batch_total" | "check_interval", value: string | boolean) => void;
  addRegisterProvider: () => void;
  updateRegisterProvider: (index: number, updates: Record<string, unknown>) => void;
  deleteRegisterProvider: (index: number) => void;
  saveRegister: () => Promise<void>;
  toggleRegister: () => Promise<void>;
  resetRegister: () => Promise<void>;
  repairAbnormalRegisterAccounts: () => Promise<void>;
  testOutlookPool: () => Promise<void>;
};

export const useSettingsStore = create<SettingsStore>((set, get) => ({
  config: null,
  isLoadingConfig: true,
  isSavingConfig: false,
  isDirty: false,

  registerConfig: null,
  isLoadingRegister: true,
  isSavingRegister: false,

  initialize: async () => {
    await get().loadConfig();
  },

  loadConfig: async () => {
    const silent = get().config !== null;
    if (!silent) set({ isLoadingConfig: true });
    try {
      const data = await fetchSettingsConfig();
      set({ config: normalizeConfig(data.config), isDirty: false });
    } catch (error) {
      if (!silent) toast.error(error instanceof Error ? error.message : "加载系统配置失败");
    } finally {
      if (!silent) set({ isLoadingConfig: false });
    }
  },

  revertConfig: async () => {
    try {
      const data = await fetchSettingsConfig();
      set({ config: normalizeConfig(data.config), isDirty: false });
      toast.success("已撤销未保存修改");
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "撤销失败");
    }
  },

  saveConfig: async () => {
    const { config } = get();
    if (!config) return false;

    const sanitized: SettingsConfig = {
      ...config,
      refresh_account_interval_minute: Math.max(1, Number(config.refresh_account_interval_minute) || 60),
      image_retention_days: Math.max(1, Number(config.image_retention_days) || 15),
      image_max_storage_mb: Math.max(0, Number(config.image_max_storage_mb) || 0),
      cleanup_protect_user_images: Boolean(config.cleanup_protect_user_images ?? true),
      image_poll_timeout_secs: Math.max(1, Number(config.image_poll_timeout_secs) || 120),
      image_poll_interval_secs: Math.max(1, Number(config.image_poll_interval_secs) || 4),
      image_poll_initial_wait_secs: Math.max(0, Number(config.image_poll_initial_wait_secs) || 0),
      image_account_concurrency: Math.max(1, Number(config.image_account_concurrency) || 3),
      image_account_fallback_attempts: Math.max(1, Number(config.image_account_fallback_attempts) || 3),
      auto_remove_invalid_accounts: Boolean(config.auto_remove_invalid_accounts ?? true),
      auto_remove_rate_limited_accounts: Boolean(config.auto_remove_rate_limited_accounts ?? true),
      auto_delete_quota_zero_accounts: Boolean(config.auto_delete_quota_zero_accounts ?? true),
      auto_delete_upload_quota_zero_accounts: Boolean(config.auto_delete_upload_quota_zero_accounts ?? true),
      delete_403_consecutive: Math.max(1, Number(config.delete_403_consecutive) || 2),
      delete_timeout_consecutive: Math.max(1, Number(config.delete_timeout_consecutive) || 2),
      auto_refresh_accounts_enabled: Boolean(config.auto_refresh_accounts_enabled ?? true),
      auto_refresh_accounts_interval_minutes: Math.max(1, Number(config.auto_refresh_accounts_interval_minutes) || 60),
      auto_refresh_accounts_batch_size: Math.max(0, Number(config.auto_refresh_accounts_batch_size) || 0),
      auto_refresh_delete_failed_accounts: Boolean(config.auto_refresh_delete_failed_accounts ?? true),
      auto_refresh_trigger_refill: Boolean(config.auto_refresh_trigger_refill ?? true),
      auto_cleanup_accounts_enabled: Boolean(config.auto_cleanup_accounts_enabled ?? true),
      auto_cleanup_accounts_interval_seconds: Math.max(10, Number(config.auto_cleanup_accounts_interval_seconds) || 60),
      auto_refill_use_effective_available: Boolean(config.auto_refill_use_effective_available ?? true),
      proxy: String(config.proxy || "").trim(),
      image_route_strategy: String(config.image_route_strategy || "web_first").trim(),
      base_url: String(config.base_url || "").trim(),
      global_system_prompt: String(config.global_system_prompt || "").trim(),
      sensitive_words: (config.sensitive_words || []).map((item) => String(item).trim()).filter(Boolean),
    };

    delete sanitized.backup;
    delete sanitized.backup_state;

    set({ isSavingConfig: true });
    try {
      const data = await updateSettingsConfig(sanitized);
      set({ config: normalizeConfig(data.config), isDirty: false });
      toast.success("配置已保存");
      return true;
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "保存系统配置失败");
      return false;
    } finally {
      set({ isSavingConfig: false });
    }
  },

  setRefreshAccountIntervalMinute: (value) => {
    set((state) => state.config ? { config: { ...state.config, refresh_account_interval_minute: value }, isDirty: true } : {});
  },
  setImageRetentionDays: (value) => {
    set((state) => state.config ? { config: { ...state.config, image_retention_days: value }, isDirty: true } : {});
  },
  setImageMaxStorageMB: (value) => {
    set((state) => state.config ? { config: { ...state.config, image_max_storage_mb: value }, isDirty: true } : {});
  },
  setCleanupProtectUserImages: (value) => {
    set((state) => state.config ? { config: { ...state.config, cleanup_protect_user_images: value }, isDirty: true } : {});
  },
  setImagePollTimeoutSecs: (value) => {
    set((state) => state.config ? { config: { ...state.config, image_poll_timeout_secs: value }, isDirty: true } : {});
  },
  setImagePollIntervalSecs: (value) => {
    set((state) => state.config ? { config: { ...state.config, image_poll_interval_secs: value }, isDirty: true } : {});
  },
  setImagePollInitialWaitSecs: (value) => {
    set((state) => state.config ? { config: { ...state.config, image_poll_initial_wait_secs: value }, isDirty: true } : {});
  },
  setImageAccountConcurrency: (value) => {
    set((state) => state.config ? { config: { ...state.config, image_account_concurrency: value }, isDirty: true } : {});
  },
  setImageAccountFallbackAttempts: (value) => {
    set((state) => state.config ? { config: { ...state.config, image_account_fallback_attempts: value }, isDirty: true } : {});
  },
  setAutoRemoveInvalidAccounts: (value) => {
    set((state) => state.config ? { config: { ...state.config, auto_remove_invalid_accounts: value }, isDirty: true } : {});
  },
  setAutoRemoveRateLimitedAccounts: (value) => {
    set((state) => state.config ? { config: { ...state.config, auto_remove_rate_limited_accounts: value }, isDirty: true } : {});
  },
  setAutoDeleteQuotaZeroAccounts: (value) => {
    set((state) => state.config ? { config: { ...state.config, auto_delete_quota_zero_accounts: value }, isDirty: true } : {});
  },
  setAutoDeleteUploadQuotaZeroAccounts: (value) => {
    set((state) => state.config ? { config: { ...state.config, auto_delete_upload_quota_zero_accounts: value }, isDirty: true } : {});
  },
  setDelete403Consecutive: (value) => {
    set((state) => state.config ? { config: { ...state.config, delete_403_consecutive: value }, isDirty: true } : {});
  },
  setDeleteTimeoutConsecutive: (value) => {
    set((state) => state.config ? { config: { ...state.config, delete_timeout_consecutive: value }, isDirty: true } : {});
  },
  setAutoRefreshAccountsEnabled: (value) => {
    set((state) => state.config ? { config: { ...state.config, auto_refresh_accounts_enabled: value }, isDirty: true } : {});
  },
  setAutoRefreshAccountsIntervalMinutes: (value) => {
    set((state) => state.config ? { config: { ...state.config, auto_refresh_accounts_interval_minutes: value }, isDirty: true } : {});
  },
  setAutoRefreshAccountsBatchSize: (value) => {
    set((state) => state.config ? { config: { ...state.config, auto_refresh_accounts_batch_size: value }, isDirty: true } : {});
  },
  setAutoRefreshDeleteFailedAccounts: (value) => {
    set((state) => state.config ? { config: { ...state.config, auto_refresh_delete_failed_accounts: value }, isDirty: true } : {});
  },
  setAutoRefreshTriggerRefill: (value) => {
    set((state) => state.config ? { config: { ...state.config, auto_refresh_trigger_refill: value }, isDirty: true } : {});
  },
  setAutoCleanupAccountsEnabled: (value) => {
    set((state) => state.config ? { config: { ...state.config, auto_cleanup_accounts_enabled: value }, isDirty: true } : {});
  },
  setAutoCleanupAccountsIntervalSeconds: (value) => {
    set((state) => state.config ? { config: { ...state.config, auto_cleanup_accounts_interval_seconds: value }, isDirty: true } : {});
  },
  setAutoRefillUseEffectiveAvailable: (value) => {
    set((state) => state.config ? { config: { ...state.config, auto_refill_use_effective_available: value }, isDirty: true } : {});
  },
  setLogLevel: (level, enabled) => {
    set((state) => {
      if (!state.config) return {};
      const levels = new Set(state.config.log_levels || []);
      if (enabled) levels.add(level);
      else levels.delete(level);
      return { config: { ...state.config, log_levels: Array.from(levels) }, isDirty: true };
    });
  },
  setProxy: (value) => {
    set((state) => state.config ? { config: { ...state.config, proxy: value }, isDirty: true } : {});
  },
  setImageRouteStrategy: (value) => {
    set((state) => state.config ? { config: { ...state.config, image_route_strategy: value }, isDirty: true } : {});
  },
  setBaseUrl: (value) => {
    set((state) => state.config ? { config: { ...state.config, base_url: value }, isDirty: true } : {});
  },
  setGlobalSystemPrompt: (value) => {
    set((state) => state.config ? { config: { ...state.config, global_system_prompt: value }, isDirty: true } : {});
  },
  setSensitiveWordsText: (value) => {
    set((state) => state.config ? { config: { ...state.config, sensitive_words: value.split("\n") }, isDirty: true } : {});
  },

  loadRegister: async (silent = false) => {
    if (!silent) set({ isLoadingRegister: true });
    try {
      const data = await fetchRegisterConfig();
      set({ registerConfig: normalizeRegister(data.register) });
    } catch (error) {
      if (!silent) toast.error(error instanceof Error ? error.message : "加载注册配置失败");
    } finally {
      if (!silent) set({ isLoadingRegister: false });
    }
  },

  setRegisterConfig: (config) => {
    set({ registerConfig: normalizeRegister(config), isLoadingRegister: false });
  },
  setRegisterProxy: (value) => {
    set((state) => state.registerConfig ? { registerConfig: { ...state.registerConfig, proxy: value } } : {});
  },
  setRegisterTotal: (value) => {
    set((state) => state.registerConfig ? { registerConfig: { ...state.registerConfig, total: Number(value) || 0 } } : {});
  },
  setRegisterThreads: (value) => {
    set((state) => state.registerConfig ? { registerConfig: { ...state.registerConfig, threads: Number(value) || 0 } } : {});
  },
  setRegisterMode: (value) => {
    set((state) => state.registerConfig ? { registerConfig: { ...state.registerConfig, mode: value } } : {});
  },
  setRegisterTargetQuota: (value) => {
    set((state) => state.registerConfig ? { registerConfig: { ...state.registerConfig, target_quota: Number(value) || 0 } } : {});
  },
  setRegisterTargetAvailable: (value) => {
    set((state) => state.registerConfig ? { registerConfig: { ...state.registerConfig, target_available: Number(value) || 0 } } : {});
  },
  setRegisterCheckInterval: (value) => {
    set((state) => state.registerConfig ? { registerConfig: { ...state.registerConfig, check_interval: Number(value) || 0 } } : {});
  },
  setRegisterFixedPassword: (value) => {
    set((state) => state.registerConfig ? { registerConfig: { ...state.registerConfig, fixed_password: value } } : {});
  },
  setRegisterTaskTimeoutSeconds: (value) => {
    set((state) => state.registerConfig ? { registerConfig: { ...state.registerConfig, task_timeout_seconds: Number(value) || 0 } } : {});
  },
  setRegisterTaskStallTimeoutSeconds: (value) => {
    set((state) => state.registerConfig ? { registerConfig: { ...state.registerConfig, task_stall_timeout_seconds: Number(value) || 0 } } : {});
  },
  setRegisterMailField: (key, value) => {
    set((state) => state.registerConfig ? {
      registerConfig: {
        ...state.registerConfig,
        mail: { ...state.registerConfig.mail, [key]: Number(value) || 0 },
      },
    } : {});
  },
  setRegisterMailUseRegisterProxy: (value) => {
    set((state) => state.registerConfig ? {
      registerConfig: {
        ...state.registerConfig,
        mail: { ...state.registerConfig.mail, api_use_register_proxy: value },
      },
    } : {});
  },
  setRegisterAutoRefillField: (key, value) => {
    set((state) => {
      if (!state.registerConfig) return {};
      const current = state.registerConfig.auto_refill || {
        enabled: false,
        min_available: 30,
        batch_total: 100,
        check_interval: 300,
      };
      return {
        registerConfig: {
          ...state.registerConfig,
          auto_refill: {
            ...current,
            [key]: typeof value === "boolean" ? value : Number(value) || 0,
          },
        },
      };
    });
  },
  addRegisterProvider: () => {
    set((state) => state.registerConfig ? {
      registerConfig: {
        ...state.registerConfig,
        mail: {
          ...state.registerConfig.mail,
          providers: [
            ...(state.registerConfig.mail.providers || []),
            { enable: true, type: "tempmail_lol", api_key: "", domain: [] },
          ],
        },
      },
    } : {});
  },
  updateRegisterProvider: (index, updates) => {
    set((state) => {
      if (!state.registerConfig) return {};
      const providers = [...(state.registerConfig.mail.providers || [])];
      providers[index] = { ...(providers[index] || {}), ...updates };
      return { registerConfig: { ...state.registerConfig, mail: { ...state.registerConfig.mail, providers } } };
    });
  },
  deleteRegisterProvider: (index) => {
    set((state) => state.registerConfig ? {
      registerConfig: {
        ...state.registerConfig,
        mail: {
          ...state.registerConfig.mail,
          providers: (state.registerConfig.mail.providers || []).filter((_, itemIndex) => itemIndex !== index),
        },
      },
    } : {});
  },

  saveRegister: async () => {
    const { registerConfig } = get();
    if (!registerConfig) return;
    if (isRegisterActive(registerConfig)) {
      toast.warning("注册任务未结束，暂不能保存配置");
      return;
    }
    set({ isSavingRegister: true });
    try {
      const data = await updateRegisterConfig({
        mail: buildRegisterMailPayload(registerConfig.mail),
        proxy: registerConfig.proxy.trim(),
        total: Math.max(1, Number(registerConfig.total) || 1),
        threads: Math.max(1, Number(registerConfig.threads) || 1),
        mode: registerConfig.mode,
        target_quota: Math.max(1, Number(registerConfig.target_quota) || 1),
        target_available: Math.max(1, Number(registerConfig.target_available) || 1),
        check_interval: Math.max(1, Number(registerConfig.check_interval) || 5),
        fixed_password: registerConfig.fixed_password,
        task_timeout_seconds: Math.max(30, Number(registerConfig.task_timeout_seconds) || 300),
        task_stall_timeout_seconds: Math.max(0, Number(registerConfig.task_stall_timeout_seconds) || 60),
        auto_refill: {
          enabled: Boolean(registerConfig.auto_refill?.enabled),
          min_available: Math.max(1, Number(registerConfig.auto_refill?.min_available) || 30),
          batch_total: Math.max(1, Number(registerConfig.auto_refill?.batch_total) || 100),
          check_interval: Math.max(10, Number(registerConfig.auto_refill?.check_interval) || 300),
        },
      });
      set({ registerConfig: normalizeRegister(data.register) });
      toast.success("注册配置已保存");
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "保存注册配置失败");
    } finally {
      set({ isSavingRegister: false });
    }
  },

  toggleRegister: async () => {
    const { registerConfig } = get();
    if (!registerConfig) return;
    const runtime = getRegisterRuntimeState(registerConfig);
    if (runtime.isStopping) {
      toast.warning("注册任务正在停止清理中，请等待完成后再启动");
      return;
    }
    set({ isSavingRegister: true });
    try {
      if (!runtime.isRunning) {
        await updateRegisterConfig({
          mail: buildRegisterMailPayload(registerConfig.mail),
          proxy: registerConfig.proxy.trim(),
          total: Math.max(1, Number(registerConfig.total) || 1),
          threads: Math.max(1, Number(registerConfig.threads) || 1),
          mode: registerConfig.mode,
          target_quota: Math.max(1, Number(registerConfig.target_quota) || 1),
          target_available: Math.max(1, Number(registerConfig.target_available) || 1),
          check_interval: Math.max(1, Number(registerConfig.check_interval) || 5),
          fixed_password: registerConfig.fixed_password,
          task_timeout_seconds: Math.max(30, Number(registerConfig.task_timeout_seconds) || 300),
          task_stall_timeout_seconds: Math.max(0, Number(registerConfig.task_stall_timeout_seconds) || 60),
          auto_refill: {
            enabled: Boolean(registerConfig.auto_refill?.enabled),
            min_available: Math.max(1, Number(registerConfig.auto_refill?.min_available) || 30),
            batch_total: Math.max(1, Number(registerConfig.auto_refill?.batch_total) || 100),
            check_interval: Math.max(10, Number(registerConfig.auto_refill?.check_interval) || 300),
          },
        });
      }
      const data = runtime.isRunning ? await stopRegister() : await startRegister();
      const nextRegister = normalizeRegister(data.register);
      const nextRuntime = getRegisterRuntimeState(nextRegister);
      set({ registerConfig: nextRegister });
      if (runtime.isRunning) {
        toast.success("已请求停止注册任务");
      } else if (nextRuntime.isStopping) {
        toast.warning("注册任务仍在停止清理中，请稍后再启动");
      } else if (nextRuntime.isRunning) {
        toast.success("已请求启动注册任务");
      } else {
        toast.warning("注册任务未启动，请检查注册执行器日志");
      }
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "切换注册状态失败");
    } finally {
      set({ isSavingRegister: false });
    }
  },

  resetRegister: async () => {
    const { registerConfig } = get();
    if (isRegisterActive(registerConfig)) {
      toast.warning("注册任务未结束，暂不能重置统计");
      return;
    }
    set({ isSavingRegister: true });
    try {
      const data = await resetRegisterApi();
      set({ registerConfig: normalizeRegister(data.register) });
      toast.success("注册统计已重置");
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "重置注册统计失败");
    } finally {
      set({ isSavingRegister: false });
    }
  },

  repairAbnormalRegisterAccounts: async () => {
    const { registerConfig } = get();
    if (!registerConfig) return;
    const runtime = getRegisterRuntimeState(registerConfig);
    if (runtime.isStopping) {
      toast.warning("注册任务正在停止清理中，请等待完成后再操作");
      return;
    }
    if (runtime.isRunning && !runtime.isRepairing) {
      toast.warning("注册任务运行中，不能同时启动异常账号修复");
      return;
    }
    set({ isSavingRegister: true });
    try {
      if (runtime.isRepairing) {
        const data = await stopRegister();
        set({ registerConfig: normalizeRegister(data.register) });
        toast.success("已请求停止异常账号修复");
        return;
      }
      const data = await repairAbnormalAccounts();
      set({ registerConfig: normalizeRegister(data.register) });
      toast.success("已请求启动异常账号修复");
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "启动异常账号修复失败");
    } finally {
      set({ isSavingRegister: false });
    }
  },
  testOutlookPool: async () => {
    set({ isSavingRegister: true });
    try {
      const data = await testRegisterOutlookPool(5);
      const result = data.result;
      toast.success(`Outlook 检测完成：成功 ${result.ok}/${result.checked}，邮箱池 ${result.total} 个`);
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "Outlook 邮箱池检测失败");
    } finally {
      set({ isSavingRegister: false });
    }
  },
}));
