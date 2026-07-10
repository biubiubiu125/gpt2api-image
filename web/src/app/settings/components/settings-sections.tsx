
"use client";

import { LoaderCircle, PlugZap } from "lucide-react";
import { useState } from "react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { testProxy, type ProxyTestResult } from "@/lib/api";

import { useSettingsStore } from "../store";

const INPUT_CLASS = "h-10 rounded-xl border-stone-200 bg-white";
const LABEL_CLASS = "text-sm text-stone-700";
const HELP_CLASS = "text-xs text-stone-500";
const TILE_CLASS = "rounded-xl border border-stone-200 bg-white px-4 py-3";

export function AccountSection() {
  const config = useSettingsStore((s) => s.config);
  const setRefreshAccountIntervalMinute = useSettingsStore((s) => s.setRefreshAccountIntervalMinute);
  const setAutoRemoveInvalidAccounts = useSettingsStore((s) => s.setAutoRemoveInvalidAccounts);
  const setAutoRemoveRateLimitedAccounts = useSettingsStore((s) => s.setAutoRemoveRateLimitedAccounts);
  const setAutoDeleteQuotaZeroAccounts = useSettingsStore((s) => s.setAutoDeleteQuotaZeroAccounts);
  const setAutoDeleteUploadQuotaZeroAccounts = useSettingsStore((s) => s.setAutoDeleteUploadQuotaZeroAccounts);
  const setDelete403Consecutive = useSettingsStore((s) => s.setDelete403Consecutive);
  const setDeleteTimeoutConsecutive = useSettingsStore((s) => s.setDeleteTimeoutConsecutive);
  const setAutoRefreshAccountsEnabled = useSettingsStore((s) => s.setAutoRefreshAccountsEnabled);
  const setAutoRefreshAccountsIntervalMinutes = useSettingsStore((s) => s.setAutoRefreshAccountsIntervalMinutes);
  const setAutoRefreshAccountsBatchSize = useSettingsStore((s) => s.setAutoRefreshAccountsBatchSize);
  const setAutoRefreshDeleteFailedAccounts = useSettingsStore((s) => s.setAutoRefreshDeleteFailedAccounts);
  const setAutoRefreshTriggerRefill = useSettingsStore((s) => s.setAutoRefreshTriggerRefill);
  const setAutoCleanupAccountsEnabled = useSettingsStore((s) => s.setAutoCleanupAccountsEnabled);
  const setAutoCleanupAccountsIntervalSeconds = useSettingsStore((s) => s.setAutoCleanupAccountsIntervalSeconds);
  const setAutoRefillUseEffectiveAvailable = useSettingsStore((s) => s.setAutoRefillUseEffectiveAvailable);

  return (
    <div className="space-y-4">
      <div className="grid gap-4 md:grid-cols-2">
        <div className="space-y-2">
          <label className={LABEL_CLASS}>{"\u8d26\u53f7\u72b6\u6001\u5237\u65b0\u95f4\u9694\uff08\u5206\u949f\uff09"}</label>
          <Input value={String(config?.refresh_account_interval_minute || "")} onChange={(e) => setRefreshAccountIntervalMinute(e.target.value)} placeholder="60" className={INPUT_CLASS} />
          <p className={HELP_CLASS}>{"\u7528\u4e8e\u9650\u6d41\u8d26\u53f7\u6062\u590d\u68c0\u67e5\uff1b\u666e\u901a\u4fe1\u606f\u5237\u65b0\u4f7f\u7528\u4e0b\u9762\u7684\u81ea\u52a8\u5237\u65b0\u8bbe\u7f6e\u3002"}</p>
        </div>
        <div className="space-y-2">
          <label className={LABEL_CLASS}>{"\u81ea\u52a8\u6e05\u7406\u68c0\u67e5\u95f4\u9694\uff08\u79d2\uff09"}</label>
          <Input value={String(config?.auto_cleanup_accounts_interval_seconds || "")} onChange={(e) => setAutoCleanupAccountsIntervalSeconds(e.target.value)} placeholder="60" className={INPUT_CLASS} />
          <p className={HELP_CLASS}>{"\u5b9a\u65f6\u6e05\u7406 pending_delete\u3001\u989d\u5ea6\u4e3a 0\u3001\u8fde\u7eed 403/timeout \u7b49\u4e0d\u53ef\u7528\u8d26\u53f7\u3002"}</p>
        </div>
        <div className="space-y-2">
          <label className={LABEL_CLASS}>{"\u81ea\u52a8\u5237\u65b0\u8d26\u53f7\u4fe1\u606f\u95f4\u9694\uff08\u5206\u949f\uff09"}</label>
          <Input value={String(config?.auto_refresh_accounts_interval_minutes || "")} onChange={(e) => setAutoRefreshAccountsIntervalMinutes(e.target.value)} placeholder="60" className={INPUT_CLASS} />
          <p className={HELP_CLASS}>{"\u5b9a\u65f6\u5237\u65b0\u6240\u6709\u8d26\u53f7\u4fe1\u606f\u548c\u989d\u5ea6\uff0c\u5237\u65b0\u6210\u529f\u4f1a\u6e05\u9664\u5237\u65b0\u9519\u8bef\u3002"}</p>
        </div>
        <div className="space-y-2">
          <label className={LABEL_CLASS}>{"\u6bcf\u8f6e\u5237\u65b0\u6570\u91cf"}</label>
          <Input value={String(config?.auto_refresh_accounts_batch_size ?? "")} onChange={(e) => setAutoRefreshAccountsBatchSize(e.target.value)} placeholder="0" className={INPUT_CLASS} />
          <p className={HELP_CLASS}>{"0 \u8868\u793a\u6bcf\u8f6e\u5237\u65b0\u5168\u90e8\u8d26\u53f7\uff1b\u5927\u53f7\u6c60\u5efa\u8bae\u5206\u6279\u8f6e\u8f6c\u3002"}</p>
        </div>
        <div className="space-y-2">
          <label className={LABEL_CLASS}>{"403 \u8fde\u7eed\u5220\u9664\u9608\u503c"}</label>
          <Input value={String(config?.delete_403_consecutive || "")} onChange={(e) => setDelete403Consecutive(e.target.value)} placeholder="2" className={INPUT_CLASS} />
          <p className={HELP_CLASS}>{"\u9047\u5230 unusual activity\u3001Cloudflare\u3001turnstile\u3001captcha \u7b49\u6b21\u6570\u8fbe\u5230\u9608\u503c\u540e\u5220\u9664\u3002"}</p>
        </div>
        <div className="space-y-2">
          <label className={LABEL_CLASS}>{"\u8d85\u65f6\u8fde\u7eed\u5220\u9664\u9608\u503c"}</label>
          <Input value={String(config?.delete_timeout_consecutive || "")} onChange={(e) => setDeleteTimeoutConsecutive(e.target.value)} placeholder="2" className={INPUT_CLASS} />
          <p className={HELP_CLASS}>{"\u9047\u5230 context deadline exceeded\u3001SSE timed out\u30015xx \u7b49\u6b21\u6570\u8fbe\u5230\u9608\u503c\u540e\u5220\u9664\u3002"}</p>
        </div>
      </div>

      <div className="grid gap-3 md:grid-cols-2">
        <label className={`flex items-center gap-3 ${TILE_CLASS} text-sm text-stone-700`}><Checkbox checked={Boolean(config?.auto_remove_invalid_accounts ?? true)} onCheckedChange={(c) => setAutoRemoveInvalidAccounts(Boolean(c))} />{"\u81ea\u52a8\u5220\u9664\u5f02\u5e38\u8d26\u53f7"}</label>
        <label className={`flex items-center gap-3 ${TILE_CLASS} text-sm text-stone-700`}><Checkbox checked={Boolean(config?.auto_remove_rate_limited_accounts ?? true)} onCheckedChange={(c) => setAutoRemoveRateLimitedAccounts(Boolean(c))} />{"\u81ea\u52a8\u5220\u9664\u9650\u6d41\u8d26\u53f7"}</label>
        <label className={`flex items-center gap-3 ${TILE_CLASS} text-sm text-stone-700`}><Checkbox checked={Boolean(config?.auto_delete_quota_zero_accounts ?? true)} onCheckedChange={(c) => setAutoDeleteQuotaZeroAccounts(Boolean(c))} />{"\u56fe\u7247\u989d\u5ea6\u4e3a 0 \u81ea\u52a8\u5220\u9664"}</label>
        <label className={`flex items-center gap-3 ${TILE_CLASS} text-sm text-stone-700`}><Checkbox checked={Boolean(config?.auto_delete_upload_quota_zero_accounts ?? true)} onCheckedChange={(c) => setAutoDeleteUploadQuotaZeroAccounts(Boolean(c))} />{"\u4e0a\u4f20\u989d\u5ea6\u4e3a 0 \u81ea\u52a8\u5220\u9664"}</label>
        <label className={`flex items-center gap-3 ${TILE_CLASS} text-sm text-stone-700`}><Checkbox checked={Boolean(config?.auto_refresh_accounts_enabled ?? true)} onCheckedChange={(c) => setAutoRefreshAccountsEnabled(Boolean(c))} />{"\u542f\u7528\u5b9a\u65f6\u5237\u65b0\u8d26\u53f7\u4fe1\u606f\u548c\u989d\u5ea6"}</label>
        <label className={`flex items-center gap-3 ${TILE_CLASS} text-sm text-stone-700`}><Checkbox checked={Boolean(config?.auto_refresh_delete_failed_accounts ?? true)} onCheckedChange={(c) => setAutoRefreshDeleteFailedAccounts(Boolean(c))} />{"\u5237\u65b0\u5931\u8d25\u81ea\u52a8\u5220\u9664\u8d26\u53f7"}</label>
        <label className={`flex items-center gap-3 ${TILE_CLASS} text-sm text-stone-700`}><Checkbox checked={Boolean(config?.auto_refresh_trigger_refill ?? true)} onCheckedChange={(c) => setAutoRefreshTriggerRefill(Boolean(c))} />{"\u5237\u65b0\u6e05\u7406\u540e\u89e6\u53d1\u8865\u53f7"}</label>
        <label className={`flex items-center gap-3 ${TILE_CLASS} text-sm text-stone-700`}><Checkbox checked={Boolean(config?.auto_cleanup_accounts_enabled ?? true)} onCheckedChange={(c) => setAutoCleanupAccountsEnabled(Boolean(c))} />{"\u542f\u7528\u5b9a\u65f6\u6e05\u7406\u5f02\u5e38\u8d26\u53f7"}</label>
        <label className={`flex items-center gap-3 ${TILE_CLASS} text-sm text-stone-700 md:col-span-2`}><Checkbox checked={Boolean(config?.auto_refill_use_effective_available ?? true)} onCheckedChange={(c) => setAutoRefillUseEffectiveAvailable(Boolean(c))} />{"\u6309\u6709\u6548\u53ef\u7528\u8d26\u53f7\u6570\u89e6\u53d1\u81ea\u52a8\u8865\u53f7"}</label>
      </div>
    </div>
  );
}

export function NetworkSection() {
  const [isTestingProxy, setIsTestingProxy] = useState(false);
  const [proxyTestResult, setProxyTestResult] = useState<ProxyTestResult | null>(null);
  const config = useSettingsStore((s) => s.config);
  const setProxy = useSettingsStore((s) => s.setProxy);

  const handleTestProxy = async () => {
    const candidate = String(config?.proxy || "").trim();
    if (!candidate) {
      toast.error("\u8bf7\u5148\u586b\u5199\u4ee3\u7406\u5730\u5740");
      return;
    }
    setIsTestingProxy(true);
    setProxyTestResult(null);
    try {
      const data = await testProxy(candidate);
      setProxyTestResult(data.result);
      if (data.result.ok) {
        const loc = [data.result.country, data.result.colo].filter(Boolean).join(" / ");
        toast.success(`\u4ee3\u7406\u53ef\u7528\uff0c\u5ef6\u8fdf ${data.result.latency_ms} ms${loc ? `\uff0c\u4f4d\u7f6e ${loc}` : ""}`);
      } else {
        toast.error(`\u4ee3\u7406\u4e0d\u53ef\u7528\uff1a${data.result.error ?? "\u672a\u77e5\u9519\u8bef"}`);
      }
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "\u6d4b\u8bd5\u4ee3\u7406\u5931\u8d25");
    } finally {
      setIsTestingProxy(false);
    }
  };

  return (
    <div className="space-y-3">
      <label className={LABEL_CLASS}>{"\u5168\u5c40\u4ee3\u7406"}</label>
      <Input
        value={String(config?.proxy || "")}
        onChange={(e) => {
          setProxy(e.target.value);
          setProxyTestResult(null);
        }}
        placeholder="http://127.0.0.1:7890"
        className={INPUT_CLASS}
      />
      <p className={HELP_CLASS}>{"\u7559\u7a7a\u8868\u793a\u4e0d\u4f7f\u7528\u4ee3\u7406\u3002\u4ee3\u7406\u4f1a\u540c\u65f6\u5f71\u54cd\u751f\u56fe\u8bf7\u6c42\u548c OpenAI \u4e0a\u6e38\u8f6c\u53d1\u3002"}</p>
      {proxyTestResult ? (
        <div className={`rounded-xl border px-3 py-2 text-xs leading-6 ${proxyTestResult.ok ? "border-emerald-200 bg-emerald-50 text-emerald-800" : "border-rose-200 bg-rose-50 text-rose-800"}`}>
          {proxyTestResult.ok ? (
            <div className="space-y-1">
              <div>
                {"\u51fa\u53e3 IP\uff1a"}{proxyTestResult.ip || "-"}
                {proxyTestResult.country ? `\uff0c\u56fd\u5bb6/\u5730\u533a\uff1a${proxyTestResult.country}` : ""}
                {proxyTestResult.colo ? `\uff0c\u8282\u70b9\uff1a${proxyTestResult.colo}` : ""}
                {`\uff0c\u5ef6\u8fdf\uff1a${proxyTestResult.latency_ms} ms`}
              </div>
              <div>{"\u68c0\u6d4b\u76ee\u6807\uff1a"}{proxyTestResult.target || "https://www.cloudflare.com/cdn-cgi/trace"}</div>
            </div>
          ) : (
            `\u4ee3\u7406\u4e0d\u53ef\u7528\uff1a${proxyTestResult.error ?? "\u672a\u77e5\u9519\u8bef"}\uff08\u7528\u65f6 ${proxyTestResult.latency_ms} ms\uff09`
          )}
        </div>
      ) : null}
      <div className="flex justify-end">
        <Button type="button" variant="outline" className="h-9 rounded-xl border-stone-200 bg-white px-4 text-stone-700" onClick={() => void handleTestProxy()} disabled={isTestingProxy}>
          {isTestingProxy ? <LoaderCircle className="size-4 animate-spin" /> : <PlugZap className="size-4" />}
          {"\u6d4b\u8bd5\u4ee3\u7406"}
        </Button>
      </div>
    </div>
  );
}

export function ImageSection() {
  const config = useSettingsStore((s) => s.config);
  const setBaseUrl = useSettingsStore((s) => s.setBaseUrl);
  const setImageRouteStrategy = useSettingsStore((s) => s.setImageRouteStrategy);
  const setImageRetentionDays = useSettingsStore((s) => s.setImageRetentionDays);
  const setImageMaxStorageMB = useSettingsStore((s) => s.setImageMaxStorageMB);
  const setCleanupProtectUserImages = useSettingsStore((s) => s.setCleanupProtectUserImages);
  const setImagePollTimeoutSecs = useSettingsStore((s) => s.setImagePollTimeoutSecs);
  const setImagePollIntervalSecs = useSettingsStore((s) => s.setImagePollIntervalSecs);
  const setImagePollInitialWaitSecs = useSettingsStore((s) => s.setImagePollInitialWaitSecs);
  const setImageAccountConcurrency = useSettingsStore((s) => s.setImageAccountConcurrency);
  const setImageAccountFallbackAttempts = useSettingsStore((s) => s.setImageAccountFallbackAttempts);

  return (
    <div className="space-y-6">
      <div className="space-y-2">
        <label className={LABEL_CLASS}>{"\u56fe\u7247\u8bbf\u95ee\u5730\u5740"}</label>
        <Input value={String(config?.base_url || "")} onChange={(e) => setBaseUrl(e.target.value)} placeholder="https://example.com" className={INPUT_CLASS} />
        <p className={HELP_CLASS}>{"\u7528\u4f5c\u751f\u6210\u7ed3\u679c URL \u7684\u524d\u7f00\u3002\u7559\u7a7a\u5219\u6309\u8bf7\u6c42 host \u81ea\u52a8\u63a8\u65ad\u3002"}</p>
      </div>

      <div className="space-y-2">
        <label className={LABEL_CLASS}>{"\u5185\u90e8\u751f\u56fe\u8def\u7531"}</label>
        <select value={String(config?.image_route_strategy || "web_first")} onChange={(e) => setImageRouteStrategy(e.target.value)} className={INPUT_CLASS + " w-full px-3 text-sm text-stone-700 md:max-w-xs"}>
          <option value="web_first">{"Web \u4f18\u5148\uff0cCodex \u5907\u7528"}</option>
          <option value="web_only">{"\u4ec5 Web \u7f51\u9875\u751f\u56fe"}</option>
          <option value="codex_first">{"Codex \u4f18\u5148\uff0cWeb \u5907\u7528"}</option>
          <option value="codex_only">{"\u4ec5 Codex"}</option>
        </select>
        <p className={HELP_CLASS}>{"\u4e0b\u6e38\u8bf7\u6c42\u6a21\u578b\u7edf\u4e00\u4e3a gpt-image-2\uff1b\u8fd9\u91cc\u53ea\u63a7\u5236\u670d\u52a1\u5185\u90e8\u9009\u62e9\u54ea\u6761\u771f\u5b9e\u4e0a\u6e38\u94fe\u8def\u3002"}</p>
      </div>

      <div className="grid gap-4 md:grid-cols-2">
        <div className="space-y-2">
          <label className={LABEL_CLASS}>{"\u56fe\u7247\u603b\u7b49\u5f85\u8d85\u65f6\uff08\u79d2\uff09"}</label>
          <Input value={String(config?.image_poll_timeout_secs || "")} onChange={(e) => setImagePollTimeoutSecs(e.target.value)} placeholder="120" className={INPUT_CLASS} />
          <p className={HELP_CLASS}>{"\u63a7\u5236 SSE \u8f6e\u8be2\u7b49\u5f85\u4e0a\u9650\uff1b\u5efa\u8bae 600 \u79d2\u4ee5\u4e0a\uff0c\u907f\u514d 90 \u79d2\u4e0a\u4e0b\u8d85\u65f6\u3002"}</p>
        </div>
        <div className="space-y-2">
          <label className={LABEL_CLASS}>{"\u56fe\u7247\u8f6e\u8be2\u95f4\u9694\uff08\u79d2\uff09"}</label>
          <Input value={String(config?.image_poll_interval_secs || "")} onChange={(e) => setImagePollIntervalSecs(e.target.value)} placeholder="4" className={INPUT_CLASS} />
          <p className={HELP_CLASS}>{"\u5f02\u6b65\u56fe\u7247\u4efb\u52a1\u67e5\u8be2\u95f4\u9694\uff0c\u8d8a\u5c0f\u8d8a\u5feb\u4f46\u4e0a\u6e38\u8bf7\u6c42\u66f4\u591a\u3002"}</p>
        </div>
        <div className="space-y-2">
          <label className={LABEL_CLASS}>{"\u8f6e\u8be2\u521d\u59cb\u7b49\u5f85\uff08\u79d2\uff09"}</label>
          <Input value={String(config?.image_poll_initial_wait_secs || "")} onChange={(e) => setImagePollInitialWaitSecs(e.target.value)} placeholder="0" className={INPUT_CLASS} />
          <p className={HELP_CLASS}>{"SSE \u5f00\u59cb\u540e\u5148\u7b49\u5f85\u4e00\u6bb5\u65f6\u95f4\u518d\u67e5\u8be2\uff0c\u53ef\u964d\u4f4e\u4e0a\u6e38 429\uff1b\u4e00\u822c 5-10 \u79d2\u3002"}</p>
        </div>
        <div className="space-y-2">
          <label className={LABEL_CLASS}>{"\u5355\u8d26\u53f7\u5e76\u53d1\u4e0a\u9650"}</label>
          <Input value={String(config?.image_account_concurrency || "")} onChange={(e) => setImageAccountConcurrency(e.target.value)} placeholder="3" className={INPUT_CLASS} />
          <p className={HELP_CLASS}>{"\u540c\u4e00\u8d26\u53f7\u540c\u65f6\u6267\u884c\u7684\u751f\u56fe\u8bf7\u6c42\u4e0a\u9650\uff0c\u8d85\u8fc7\u540e\u4f1a\u9009\u62e9\u5176\u4ed6\u8d26\u53f7\u3002"}</p>
        </div>
        <div className="space-y-2">
          <label className={LABEL_CLASS}>{"\u515c\u5e95\u8d26\u53f7\u6570\u91cf"}</label>
          <Input value={String(config?.image_account_fallback_attempts || "")} onChange={(e) => setImageAccountFallbackAttempts(e.target.value)} placeholder="3" className={INPUT_CLASS} />
          <p className={HELP_CLASS}>{"\u4e00\u6b21\u751f\u56fe\u5728\u5168\u8def\u7531\u6700\u591a\u5c1d\u8bd5\u7684\u4e0d\u540c\u8d26\u53f7\u6570\u91cf\uff0c\u5efa\u8bae\u4fdd\u6301 3\u3002"}</p>
        </div>
      </div>

      <div className="space-y-3 rounded-xl border border-stone-200 bg-stone-50/60 p-4">
        <div className="grid gap-4 md:grid-cols-2">
          <div className="space-y-2">
            <label className={LABEL_CLASS}>{"\u56fe\u7247\u4fdd\u7559\u5929\u6570"}</label>
            <Input value={String(config?.image_retention_days || "")} onChange={(e) => setImageRetentionDays(e.target.value)} placeholder="30" className={INPUT_CLASS} />
            <p className={HELP_CLASS}>{"\u8d85\u8fc7\u4fdd\u7559\u5929\u6570\u7684\u56fe\u7247\u4f1a\u88ab\u6e05\u7406\uff0c\u5f00\u542f\u4fdd\u62a4\u540e\u7528\u6237\u5df2\u4f7f\u7528\u7684\u56fe\u7247\u4e0d\u5220\u3002"}</p>
          </div>
          <div className="space-y-2">
            <label className={LABEL_CLASS}>{"\u56fe\u7247\u6700\u5927\u5b58\u50a8\uff08MB\uff09"}</label>
            <Input value={String(config?.image_max_storage_mb ?? "")} onChange={(e) => setImageMaxStorageMB(e.target.value)} placeholder="1024" className={INPUT_CLASS} />
            <p className={HELP_CLASS}>{"\u8d85\u8fc7\u5bb9\u91cf\u540e\u6309\u65f6\u95f4\u6e05\u7406\u65e7\u56fe\u7247\uff1b0 \u8868\u793a\u4e0d\u9650\u5236\u5bb9\u91cf\u3002"}</p>
          </div>
        </div>
        <label className={`flex items-start gap-3 ${TILE_CLASS} text-sm text-stone-700`}>
          <Checkbox checked={Boolean(config?.cleanup_protect_user_images ?? true)} onCheckedChange={(c) => setCleanupProtectUserImages(Boolean(c))} />
          <div className="space-y-1">
            <div className="font-medium">{"\u4fdd\u62a4\u7528\u6237\u5df2\u4f7f\u7528\u56fe\u7247"}</div>
            <div className="text-xs leading-5 text-stone-500">{"\u6e05\u7406\u56fe\u7247\u65f6\u4fdd\u7559\u5df2\u7ecf\u51fa\u73b0\u5728\u7528\u6237\u4efb\u52a1\u3001\u56fe\u5e93\u6216\u65e5\u5fd7\u4e2d\u7684\u56fe\u7247\uff0c\u907f\u514d\u5386\u53f2\u7ed3\u679c\u5931\u6548\u3002"}</div>
          </div>
        </label>
      </div>
    </div>
  );
}

export function SecuritySection() {
  const config = useSettingsStore((s) => s.config);
  const setGlobalSystemPrompt = useSettingsStore((s) => s.setGlobalSystemPrompt);
  const setSensitiveWordsText = useSettingsStore((s) => s.setSensitiveWordsText);

  return (
    <div className="space-y-6">
      <div className="space-y-2">
        <label className={LABEL_CLASS}>{"\u5168\u5c40\u9644\u52a0\u6307\u4ee4"}</label>
        <Textarea
          value={String(config?.global_system_prompt || "")}
          onChange={(e) => setGlobalSystemPrompt(e.target.value)}
          placeholder="\u4f8b\u5982\uff1a\u5148\u5224\u65ad\u7528\u6237\u63d0\u793a\u8bcd\u662f\u5426\u5408\u89c4\uff1b\u9047\u5230\u8fdd\u6cd5\u3001\u8272\u60c5\u3001\u66b4\u529b\u3001\u4ec7\u6068\u7b49\u8bf7\u6c42\u65f6\u62d2\u7edd\u56de\u7b54\u3002"
          className="min-h-28 rounded-xl border-stone-200 bg-white font-mono text-xs shadow-none"
        />
        <p className={HELP_CLASS}>{"\u6bcf\u6b21\u8bf7\u6c42\u90fd\u4f1a\u4f5c\u4e3a system \u6d88\u606f\u6ce8\u5165\u3002\u53ef\u7528\u4e8e\u5ba1\u6838\u7528\u6237\u63d0\u793a\u8bcd\u3001\u7edf\u4e00\u7ea6\u675f\u6a21\u578b\u884c\u4e3a\u6216\u56fa\u5b9a\u89d2\u8272\u8bbe\u5b9a\u3002"}</p>
      </div>
      <div className="space-y-2">
        <label className={LABEL_CLASS}>{"\u654f\u611f\u8bcd"}</label>
        <Textarea
          value={(config?.sensitive_words || []).join("\n")}
          onChange={(e) => setSensitiveWordsText(e.target.value)}
          placeholder="\u4e00\u884c\u4e00\u4e2a\uff0c\u547d\u4e2d\u5373\u62d2\u7edd"
          className="min-h-28 rounded-xl border-stone-200 bg-white font-mono text-xs shadow-none"
        />
        <p className={HELP_CLASS}>{"\u7528\u6237\u8bf7\u6c42\u5305\u542b\u4efb\u610f\u654f\u611f\u8bcd\u65f6\u76f4\u63a5\u8fd4\u56de\u62d2\u7edd\uff0c\u4e0d\u518d\u4e0b\u53d1\u5230\u751f\u56fe\u8d26\u53f7\u3002"}</p>
      </div>
    </div>
  );
}

export function LogSection() {
  const config = useSettingsStore((s) => s.config);
  const setLogLevel = useSettingsStore((s) => s.setLogLevel);
  const logLevelOptions = ["debug", "info", "warning", "error"];

  return (
    <div className="space-y-3">
      <label className={LABEL_CLASS}>{"\u63a7\u5236\u53f0\u65e5\u5fd7\u7ea7\u522b"}</label>
      <p className={HELP_CLASS}>{"\u4e0d\u9009\u62e9\u65f6\u4f7f\u7528\u9ed8\u8ba4 info / warning / error\u3002"}</p>
      <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
        {logLevelOptions.map((level) => (
          <label
            key={level}
            className={`flex items-center gap-2 ${TILE_CLASS} text-sm capitalize text-stone-700`}
          >
            <Checkbox
              checked={Boolean(config?.log_levels?.includes(level))}
              onCheckedChange={(c) => setLogLevel(level, Boolean(c))}
            />
            {level}
          </label>
        ))}
      </div>
    </div>
  );
}
