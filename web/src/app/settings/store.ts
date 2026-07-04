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
  updateRegisterConfig,
  updateSettingsConfig,
  type RegisterConfig,
  type SettingsConfig,
} from "@/lib/api";

function normalizeConfig(config: SettingsConfig): SettingsConfig {
  return {
    ...config,
    refresh_account_interval_minute: Number(config.refresh_account_interval_minute || 60),
    image_retention_days: Number(config.image_retention_days || 15),
    cleanup_protect_user_images: Boolean(config.cleanup_protect_user_images ?? true),
    image_poll_timeout_secs: Number(config.image_poll_timeout_secs || 120),
    image_poll_interval_secs: Number(config.image_poll_interval_secs || 4),
    image_poll_initial_wait_secs: Number(config.image_poll_initial_wait_secs || 0),
    image_account_concurrency: Number(config.image_account_concurrency || 3),
    auto_remove_invalid_accounts: Boolean(config.auto_remove_invalid_accounts),
    auto_remove_rate_limited_accounts: Boolean(config.auto_remove_rate_limited_accounts),
    log_levels: Array.isArray(config.log_levels) ? config.log_levels : [],
    proxy: typeof config.proxy === "string" ? config.proxy : "",
    base_url: typeof config.base_url === "string" ? config.base_url : "",
    global_system_prompt: String(config.global_system_prompt || ""),
    sensitive_words: Array.isArray(config.sensitive_words) ? config.sensitive_words : [],
    ai_review: {
      enabled: Boolean(config.ai_review?.enabled),
      base_url: String(config.ai_review?.base_url || ""),
      api_key: String(config.ai_review?.api_key || ""),
      model: String(config.ai_review?.model || ""),
      prompt: String(config.ai_review?.prompt || ""),
    },
  };
}

function normalizeRegister(config: RegisterConfig): RegisterConfig {
  return {
    ...config,
    mail: {
      request_timeout: Number(config.mail?.request_timeout || 30),
      wait_timeout: Number(config.mail?.wait_timeout || 180),
      wait_interval: Number(config.mail?.wait_interval || 5),
      providers: Array.isArray(config.mail?.providers) ? config.mail.providers : [],
    },
    proxy: String(config.proxy || ""),
    total: Number(config.total || 1),
    threads: Number(config.threads || 1),
    mode: config.mode || "total",
    target_quota: Number(config.target_quota || 1),
    target_available: Number(config.target_available || 1),
    check_interval: Number(config.check_interval || 5),
    fixed_password: String(config.fixed_password || ""),
    stats: {
      ...(config.stats || {}),
      success: Number(config.stats?.success || 0),
      fail: Number(config.stats?.fail || 0),
      done: Number(config.stats?.done || 0),
      running: Number(config.stats?.running || 0),
      threads: Number(config.stats?.threads || config.threads || 1),
    },
    logs: Array.isArray(config.logs) ? config.logs : [],
  };
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
  setCleanupProtectUserImages: (value: boolean) => void;
  setImagePollTimeoutSecs: (value: string) => void;
  setImagePollIntervalSecs: (value: string) => void;
  setImagePollInitialWaitSecs: (value: string) => void;
  setImageAccountConcurrency: (value: string) => void;
  setAutoRemoveInvalidAccounts: (value: boolean) => void;
  setAutoRemoveRateLimitedAccounts: (value: boolean) => void;
  setLogLevel: (level: string, enabled: boolean) => void;
  setProxy: (value: string) => void;
  setBaseUrl: (value: string) => void;
  setGlobalSystemPrompt: (value: string) => void;
  setSensitiveWordsText: (value: string) => void;
  setAIReviewField: (key: "enabled" | "base_url" | "api_key" | "model" | "prompt", value: string | boolean) => void;

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
  setRegisterMailField: (key: "request_timeout" | "wait_timeout" | "wait_interval", value: string) => void;
  addRegisterProvider: () => void;
  updateRegisterProvider: (index: number, updates: Record<string, unknown>) => void;
  deleteRegisterProvider: (index: number) => void;
  saveRegister: () => Promise<void>;
  toggleRegister: () => Promise<void>;
  resetRegister: () => Promise<void>;
  repairAbnormalRegisterAccounts: () => Promise<void>;
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
      cleanup_protect_user_images: Boolean(config.cleanup_protect_user_images ?? true),
      image_poll_timeout_secs: Math.max(1, Number(config.image_poll_timeout_secs) || 120),
      image_poll_interval_secs: Math.max(1, Number(config.image_poll_interval_secs) || 4),
      image_poll_initial_wait_secs: Math.max(0, Number(config.image_poll_initial_wait_secs) || 0),
      image_account_concurrency: Math.max(1, Number(config.image_account_concurrency) || 3),
      auto_remove_invalid_accounts: Boolean(config.auto_remove_invalid_accounts),
      auto_remove_rate_limited_accounts: Boolean(config.auto_remove_rate_limited_accounts),
      proxy: String(config.proxy || "").trim(),
      base_url: String(config.base_url || "").trim(),
      global_system_prompt: String(config.global_system_prompt || "").trim(),
      sensitive_words: (config.sensitive_words || []).map((item) => String(item).trim()).filter(Boolean),
      ai_review: {
        enabled: Boolean(config.ai_review?.enabled),
        base_url: String(config.ai_review?.base_url || "").trim(),
        api_key: String(config.ai_review?.api_key || "").trim(),
        model: String(config.ai_review?.model || "").trim(),
        prompt: String(config.ai_review?.prompt || "").trim(),
      },
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
  setAutoRemoveInvalidAccounts: (value) => {
    set((state) => state.config ? { config: { ...state.config, auto_remove_invalid_accounts: value }, isDirty: true } : {});
  },
  setAutoRemoveRateLimitedAccounts: (value) => {
    set((state) => state.config ? { config: { ...state.config, auto_remove_rate_limited_accounts: value }, isDirty: true } : {});
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
  setBaseUrl: (value) => {
    set((state) => state.config ? { config: { ...state.config, base_url: value }, isDirty: true } : {});
  },
  setGlobalSystemPrompt: (value) => {
    set((state) => state.config ? { config: { ...state.config, global_system_prompt: value }, isDirty: true } : {});
  },
  setSensitiveWordsText: (value) => {
    set((state) => state.config ? { config: { ...state.config, sensitive_words: value.split("\n") }, isDirty: true } : {});
  },
  setAIReviewField: (key, value) => {
    set((state) => state.config ? { config: { ...state.config, ai_review: { ...(state.config.ai_review || {}), [key]: value } }, isDirty: true } : {});
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
  setRegisterMailField: (key, value) => {
    set((state) => state.registerConfig ? {
      registerConfig: {
        ...state.registerConfig,
        mail: { ...state.registerConfig.mail, [key]: Number(value) || 0 },
      },
    } : {});
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
    set({ isSavingRegister: true });
    try {
      const data = await updateRegisterConfig({
        mail: registerConfig.mail,
        proxy: registerConfig.proxy.trim(),
        total: Math.max(1, Number(registerConfig.total) || 1),
        threads: Math.max(1, Number(registerConfig.threads) || 1),
        mode: registerConfig.mode,
        target_quota: Math.max(1, Number(registerConfig.target_quota) || 1),
        target_available: Math.max(1, Number(registerConfig.target_available) || 1),
        check_interval: Math.max(1, Number(registerConfig.check_interval) || 5),
        fixed_password: registerConfig.fixed_password,
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
    set({ isSavingRegister: true });
    try {
      if (!registerConfig.enabled) {
        await updateRegisterConfig({
          mail: registerConfig.mail,
          proxy: registerConfig.proxy.trim(),
          total: Math.max(1, Number(registerConfig.total) || 1),
          threads: Math.max(1, Number(registerConfig.threads) || 1),
          mode: registerConfig.mode,
          target_quota: Math.max(1, Number(registerConfig.target_quota) || 1),
          target_available: Math.max(1, Number(registerConfig.target_available) || 1),
          check_interval: Math.max(1, Number(registerConfig.check_interval) || 5),
          fixed_password: registerConfig.fixed_password,
        });
      }
      const data = registerConfig.enabled ? await stopRegister() : await startRegister();
      set({ registerConfig: normalizeRegister(data.register) });
      toast.success(registerConfig.enabled ? "注册任务已停止" : "已请求启动注册任务");
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "切换注册状态失败");
    } finally {
      set({ isSavingRegister: false });
    }
  },

  resetRegister: async () => {
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
    set({ isSavingRegister: true });
    try {
      if (registerConfig.enabled && registerConfig.stats?.job_kind === "repair_abnormal") {
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
}));
