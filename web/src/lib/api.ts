import { httpRequest, request } from "@/lib/request";

export type AccountType = string;
export type AccountStatus = "正常" | "限流" | "异常" | "禁用";
export type ImageModel = "gpt-image-2" | "codex-gpt-image-2";
export type AuthRole = "admin" | "user";
export type AccountTier = "free" | "premium";

export type Account = {
  access_token: string;
  type: AccountType;
  source_type?: "web" | "codex" | string;
  export_type?: string;
  status: AccountStatus;
  quota: number;
  initial_quota?: number;
  image_quota_unknown?: boolean;
  email?: string | null;
  user_id?: string | null;
  limits_progress?: Array<{
    feature_name?: string;
    remaining?: number;
    reset_after?: string;
  }>;
  default_model_slug?: string | null;
  restore_at?: string | null;
  success: number;
  fail: number;
  last_used_at?: string | null;
  mailbox?: Record<string, unknown> | null;
  password?: string | null;
  refresh_token?: string | null;
  id_token?: string | null;
  created_at?: string | null;
};

export type AccountImportRecord = Record<string, unknown> & {
  access_token?: string;
  accessToken?: string;
};

type AccountListResponse = {
  items: Account[];
};

type AccountMutationResponse = {
  items: Account[];
  added?: number;
  skipped?: number;
  removed?: number;
  refreshed?: number;
  errors?: Array<{ access_token: string; error: string }>;
};

type AccountRefreshResponse = {
  items: Account[];
  refreshed: number;
  errors: Array<{ access_token: string; error: string }>;
};

type AccountUpdateResponse = {
  item: Account;
  items: Account[];
};

export type SettingsConfig = {
  proxy: string;
  base_url?: string;
  global_system_prompt?: string;
  sensitive_words?: string[];
  ai_review?: {
    enabled?: boolean;
    base_url?: string;
    api_key?: string;
    model?: string;
    prompt?: string;
  };
  refresh_account_interval_minute?: number | string;
  image_retention_days?: number | string;
  cleanup_protect_user_images?: boolean;
  image_poll_timeout_secs?: number | string;
  image_poll_interval_secs?: number | string;
  image_poll_initial_wait_secs?: number | string;
  image_account_concurrency?: number | string;
  auto_remove_invalid_accounts?: boolean;
  auto_remove_rate_limited_accounts?: boolean;
  log_levels?: string[];
  [key: string]: unknown;
};

export type ManagedImage = {
  rel: string;
  path?: string;
  name: string;
  date: string;
  size: number;
  url: string;
  thumbnail_url?: string;
  created_at: string;
  width?: number;
  height?: number;
  tags?: string[];
  owner_id?: string;
  // 后端在 list_images 里打的标记：true 表示该 owner_id 落在主密钥集合里。
  // 前端用它把 badge 显示成"主密钥"，避免暴露具体管理密钥 id。
  is_admin_owner?: boolean;
  // 生成时记下来的 prompt 原文（image_prompts.json）。老数据为空字符串。
  // 给图片管理和一键复用使用；为空时前端按无提示词处理。
  prompt?: string;
};

export type ImageOwner = {
  id: string;
  name: string;
  deleted: boolean;
  count: number;
};

export type SystemLog = {
  id: string;
  time: string;
  type: "call" | "account" | string;
  summary?: string;
  detail?: Record<string, unknown>;
  [key: string]: unknown;
};

export type ImageResponse = {
  created: number;
  data: Array<{ b64_json?: string; url?: string; revised_prompt?: string }>;
};

export type ImageTask = {
  id: string;
  client_task_id?: string;
  status: "queued" | "running" | "success" | "error" | "canceled";
  mode: "generate" | "edit";
  model?: ImageModel;
  size?: string;
  resolution?: string;
  created_at: string;
  updated_at: string;
  data?: Array<{ b64_json?: string; url?: string; revised_prompt?: string }>;
  error?: string;
};

type ImageTaskListResponse = {
  items: ImageTask[];
  missing_ids: string[];
};

type ImageTaskCancelResponse = {
  canceled: string[];
  skipped: string[];
  missing_ids: string[];
};

export type LoginResponse = {
  ok: boolean;
  version: string;
  role: AuthRole;
  subject_id: string;
  name: string;
  account_tier?: AccountTier;
  can_use_paid_image_accounts?: boolean;
  can_use_high_resolution?: boolean;
};

export type UserKey = {
  id: string;
  name: string;
  role: AuthRole;
  enabled: boolean;
  created_at: string | null;
  last_used_at: string | null;
  account_tier: AccountTier;
  can_use_paid_image_accounts?: boolean;
  can_use_high_resolution?: boolean;
  // 后端是否仍持有原文密钥；老数据只存 key_hash 时为 false，前端据此切到"重置后回显"流程。
  key_visible: boolean;
  image_daily_quota: number;
  image_daily_used: number;
  image_daily_unlimited: boolean;
  image_daily_remaining: number | null;
  image_monthly_quota: number;
  image_monthly_used: number;
  image_monthly_unlimited: boolean;
  image_monthly_remaining: number | null;
  image_total_quota: number;
  image_total_used: number;
  image_total_unlimited: boolean;
  image_total_remaining: number | null;
  chat_daily_quota: number;
  chat_daily_used: number;
  chat_daily_unlimited: boolean;
  chat_daily_remaining: number | null;
  chat_monthly_quota: number;
  chat_monthly_used: number;
  chat_monthly_unlimited: boolean;
  chat_monthly_remaining: number | null;
  chat_total_quota: number;
  chat_total_used: number;
  chat_total_unlimited: boolean;
  chat_total_remaining: number | null;
};

export type AuthIdentity = {
  id: string;
  name: string;
  role: AuthRole;
  account_tier?: AccountTier;
  can_use_paid_image_accounts?: boolean;
  can_use_high_resolution?: boolean;
  image_daily_quota: number;
  image_daily_used: number;
  image_daily_unlimited: boolean;
  image_daily_remaining: number | null;
  image_monthly_quota: number;
  image_monthly_used: number;
  image_monthly_unlimited: boolean;
  image_monthly_remaining: number | null;
  image_total_quota: number;
  image_total_used: number;
  image_total_unlimited: boolean;
  image_total_remaining: number | null;
  chat_daily_quota: number;
  chat_daily_used: number;
  chat_daily_unlimited: boolean;
  chat_daily_remaining: number | null;
  chat_monthly_quota: number;
  chat_monthly_used: number;
  chat_monthly_unlimited: boolean;
  chat_monthly_remaining: number | null;
  chat_total_quota: number;
  chat_total_used: number;
  chat_total_unlimited: boolean;
  chat_total_remaining: number | null;
};

export type RegisterConfig = {
  enabled: boolean;
  mail: {
    request_timeout: number;
    wait_timeout: number;
    wait_interval: number;
    providers: Array<Record<string, unknown>>;
  };
  proxy: string;
  total: number;
  threads: number;
  mode: "total" | "quota" | "available";
  target_quota: number;
  target_available: number;
  check_interval: number;
  fixed_password: string;
  stats: {
    job_id?: string;
    job_kind?: string;
    success: number;
    fail: number;
    done: number;
    running: number;
    threads: number;
    elapsed_seconds?: number;
    avg_seconds?: number;
    success_rate?: number;
    current_quota?: number;
    current_available?: number;
    started_at?: string;
    updated_at?: string;
    finished_at?: string;
  };
  logs?: Array<{
    time: string;
    text: string;
    level: string;
  }>;
};

export async function login(authKey: string) {
  const normalizedAuthKey = String(authKey || "").trim();
  return httpRequest<LoginResponse>("/auth/login", {
    method: "POST",
    body: {},
    headers: {
      Authorization: `Bearer ${normalizedAuthKey}`,
    },
    redirectOnUnauthorized: false,
  });
}

export async function fetchAccounts() {
  return httpRequest<AccountListResponse>("/api/accounts");
}

export async function createAccounts(tokens: string[], sourceType = "web", accountRecords: AccountImportRecord[] = []) {
  return httpRequest<AccountMutationResponse>("/api/accounts", {
    method: "POST",
    body: { tokens, source_type: sourceType, account_records: accountRecords },
  });
}

export async function deleteAccounts(tokens: string[]) {
  return deleteAccountsWithOptions(tokens);
}

export async function deleteAccountsWithOptions(tokens: string[], deleteMailboxes = false) {
  return httpRequest<AccountMutationResponse>("/api/accounts", {
    method: "DELETE",
    body: { tokens, delete_mailboxes: deleteMailboxes },
  });
}

export async function refreshAccounts(accessTokens: string[]) {
  return httpRequest<AccountRefreshResponse>("/api/accounts/refresh", {
    method: "POST",
    body: { access_tokens: accessTokens },
  });
}

export async function updateAccount(
  accessToken: string,
  updates: {
    type?: AccountType;
    status?: AccountStatus;
    quota?: number;
  },
) {
  return httpRequest<AccountUpdateResponse>("/api/accounts/update", {
    method: "POST",
    body: {
      access_token: accessToken,
      ...updates,
    },
  });
}

export async function generateImage(prompt: string, model?: ImageModel, size?: string, resolution?: string) {
  return httpRequest<ImageResponse>(
    "/v1/images/generations",
    {
      method: "POST",
      body: {
        prompt,
        ...(model ? { model } : {}),
        ...(size ? { size } : {}),
        ...(resolution ? { resolution } : {}),
        n: 1,
        response_format: "b64_json",
      },
    },
  );
}

export async function editImage(files: File | File[], prompt: string, model?: ImageModel, size?: string, resolution?: string) {
  const formData = new FormData();
  const uploadFiles = Array.isArray(files) ? files : [files];

  uploadFiles.forEach((file) => {
    formData.append("image", file);
  });
  formData.append("prompt", prompt);
  if (model) {
    formData.append("model", model);
  }
  if (size) {
    formData.append("size", size);
  }
  if (resolution) {
    formData.append("resolution", resolution);
  }
  formData.append("n", "1");

  return httpRequest<ImageResponse>(
    "/v1/images/edits",
    {
      method: "POST",
      body: formData,
    },
  );
}

export async function createImageGenerationTask(
  clientTaskId: string,
  prompt: string,
  model?: ImageModel,
  size?: string,
  resolution?: string,
) {
  return httpRequest<ImageTask>("/api/image-tasks/generations", {
    method: "POST",
    body: {
      client_task_id: clientTaskId,
      prompt,
      ...(model ? { model } : {}),
      ...(size ? { size } : {}),
      ...(resolution ? { resolution } : {}),
    },
  });
}

export async function createImageEditTask(
  clientTaskId: string,
  files: File | File[],
  prompt: string,
  model?: ImageModel,
  size?: string,
  resolution?: string,
) {
  const formData = new FormData();
  const uploadFiles = Array.isArray(files) ? files : [files];

  uploadFiles.forEach((file) => {
    formData.append("image", file);
  });
  formData.append("client_task_id", clientTaskId);
  formData.append("prompt", prompt);
  if (model) {
    formData.append("model", model);
  }
  if (size) {
    formData.append("size", size);
  }
  if (resolution) {
    formData.append("resolution", resolution);
  }

  return httpRequest<ImageTask>("/api/image-tasks/edits", {
    method: "POST",
    body: formData,
  });
}

export async function fetchImageTasks(ids: string[]) {
  const params = new URLSearchParams();
  if (ids.length > 0) {
    params.set("ids", ids.join(","));
  }
  return httpRequest<ImageTaskListResponse>(`/api/image-tasks${params.toString() ? `?${params.toString()}` : ""}`);
}

export async function cancelImageTasks(ids: string[]) {
  return httpRequest<ImageTaskCancelResponse>("/api/image-tasks/cancel", {
    method: "POST",
    body: { ids },
  });
}

export async function fetchSettingsConfig() {
  return httpRequest<{ config: SettingsConfig }>("/api/settings");
}

export async function updateSettingsConfig(settings: SettingsConfig) {
  return httpRequest<{ config: SettingsConfig }>("/api/settings", {
    method: "POST",
    body: settings,
  });
}

export async function fetchManagedImages(filters: { start_date?: string; end_date?: string; owner?: string }) {
  const params = new URLSearchParams();
  if (filters.start_date) params.set("start_date", filters.start_date);
  if (filters.end_date) params.set("end_date", filters.end_date);
  if (filters.owner) params.set("owner", filters.owner);
  return httpRequest<{ items: ManagedImage[]; groups: Array<{ date: string; items: ManagedImage[] }> }>(
    `/api/images${params.toString() ? `?${params.toString()}` : ""}`,
  );
}

export async function fetchImageOwners() {
  return httpRequest<{ items: ImageOwner[] }>("/api/images/owners");
}

export async function deleteManagedImages(body: { paths?: string[]; start_date?: string; end_date?: string; owner?: string; all_matching?: boolean }) {
  return httpRequest<{ removed: number }>("/api/images/delete", { method: "POST", body });
}

export async function downloadImages(paths: string[]) {
  const response = await request.post("/api/images/download", { paths }, { responseType: "blob" });
  const blob = response.data as Blob;
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = "images.zip";
  document.body.appendChild(a);
  a.click();
  document.body.removeChild(a);
  URL.revokeObjectURL(url);
}

export async function downloadSingleImage(path: string) {
  const response = await request.get(`/api/images/download/${path}`, { responseType: "blob" });
  const blob = response.data as Blob;
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = path.split("/").pop() || "image.png";
  document.body.appendChild(a);
  a.click();
  document.body.removeChild(a);
  URL.revokeObjectURL(url);
}

export async function fetchImageTags() {
  return httpRequest<{ tags: string[] }>("/api/images/tags");
}

export async function setImageTags(path: string, tags: string[]) {
  return httpRequest<{ ok: boolean; tags: string[] }>("/api/images/tags", {
    method: "POST",
    body: { path, tags },
  });
}

export async function deleteImageTag(tag: string) {
  return httpRequest<{ ok: boolean; removed_from: number }>(`/api/images/tags/${encodeURIComponent(tag)}`, {
    method: "DELETE",
  });
}

export async function fetchSystemLogs(filters: { type?: string; start_date?: string; end_date?: string }) {
  const params = new URLSearchParams();
  if (filters.type) params.set("type", filters.type);
  if (filters.start_date) params.set("start_date", filters.start_date);
  if (filters.end_date) params.set("end_date", filters.end_date);
  return httpRequest<{ items: SystemLog[] }>(`/api/logs${params.toString() ? `?${params.toString()}` : ""}`);
}

export async function deleteSystemLogs(ids: string[]) {
  return httpRequest<{ removed: number }>("/api/logs/delete", {
    method: "POST",
    body: { ids },
  });
}

export async function fetchUserKeys() {
  return httpRequest<{ items: UserKey[] }>("/api/auth/users");
}

export async function fetchMyIdentity() {
  return httpRequest<{ identity: AuthIdentity }>("/api/auth/me");
}

export type UserKeyCreatePayload = {
  name?: string;
  key?: string;
};

export type UserKeyUpdatePayload = {
  enabled?: boolean;
  name?: string;
  key?: string;
};

export async function createUserKey(payload: UserKeyCreatePayload) {
  return httpRequest<{ item: UserKey; key: string; items: UserKey[] }>("/api/auth/users", {
    method: "POST",
    body: {
      name: payload.name ?? "",
      ...(payload.key ? { key: payload.key } : {}),
    },
  });
}

export async function updateUserKey(keyId: string, updates: UserKeyUpdatePayload) {
  return httpRequest<{ item: UserKey; items: UserKey[] }>(`/api/auth/users/${keyId}`, {
    method: "POST",
    body: updates,
  });
}

export async function deleteUserKey(keyId: string) {
  return httpRequest<{ items: UserKey[] }>(`/api/auth/users/${keyId}`, {
    method: "DELETE",
  });
}

export async function fetchUserKeyPlaintext(keyId: string) {
  return httpRequest<{ key: string; key_visible: boolean }>(`/api/auth/users/${keyId}/key`);
}

export async function regenerateUserKey(keyId: string, customKey?: string) {
  return httpRequest<{ item: UserKey; key: string; items: UserKey[] }>(
    `/api/auth/users/${keyId}/regenerate`,
    { method: "POST", body: { key: customKey ?? "" } },
  );
}

export async function fetchRegisterConfig() {
  return httpRequest<{ register: RegisterConfig }>("/api/register");
}

export async function updateRegisterConfig(updates: Partial<RegisterConfig>) {
  return httpRequest<{ register: RegisterConfig }>("/api/register", {
    method: "POST",
    body: updates,
  });
}

export async function startRegister() {
  return httpRequest<{ register: RegisterConfig }>("/api/register/start", { method: "POST" });
}

export async function stopRegister() {
  return httpRequest<{ register: RegisterConfig }>("/api/register/stop", { method: "POST" });
}

export async function resetRegister() {
  return httpRequest<{ register: RegisterConfig }>("/api/register/reset", { method: "POST" });
}

export async function repairAbnormalAccounts() {
  return httpRequest<{ register: RegisterConfig }>("/api/register/repair-abnormal", { method: "POST" });
}

// ── Upstream proxy ────────────────────────────────────────────────

export type ProxyTestResult = {
  ok: boolean;
  status: number;
  latency_ms: number;
  error: string | null;
};

export async function testProxy(url?: string) {
  return httpRequest<{ result: ProxyTestResult }>("/api/proxy/test", {
    method: "POST",
    body: { url: url ?? "" },
  });
}
