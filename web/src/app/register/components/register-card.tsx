"use client";

import { useEffect, useRef, useState } from "react";
import {
  AlertTriangle,
  ChevronDown,
  ChevronRight,
  LoaderCircle,
  MailCheck,
  Play,
  Plus,
  RotateCcw,
  Save,
  Settings2,
  Square,
  Terminal,
  Trash2,
  UserPlus,
  Wrench,
} from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";
import { fetchYYDSDomainBlacklist, replaceYYDSDomainBlacklist, resetYYDSDomainBlacklist } from "@/lib/api";
import { cn } from "@/lib/utils";

import { useSettingsStore } from "../../settings/store";

export function RegisterCard() {
  const config = useSettingsStore((state) => state.registerConfig);
  const isLoading = useSettingsStore((state) => state.isLoadingRegister);
  const isSaving = useSettingsStore((state) => state.isSavingRegister);
  const setProxy = useSettingsStore((state) => state.setRegisterProxy);
  const setTotal = useSettingsStore((state) => state.setRegisterTotal);
  const setThreads = useSettingsStore((state) => state.setRegisterThreads);
  const setMode = useSettingsStore((state) => state.setRegisterMode);
  const setTargetQuota = useSettingsStore((state) => state.setRegisterTargetQuota);
  const setTargetAvailable = useSettingsStore((state) => state.setRegisterTargetAvailable);
  const setCheckInterval = useSettingsStore((state) => state.setRegisterCheckInterval);
  const setFixedPassword = useSettingsStore((state) => state.setRegisterFixedPassword);
  const setTaskTimeout = useSettingsStore((state) => state.setRegisterTaskTimeoutSeconds);
  const setTaskStallTimeout = useSettingsStore((state) => state.setRegisterTaskStallTimeoutSeconds);
  const setMailField = useSettingsStore((state) => state.setRegisterMailField);
  const setMailUseRegisterProxy = useSettingsStore((state) => state.setRegisterMailUseRegisterProxy);
  const setAutoRefillField = useSettingsStore((state) => state.setRegisterAutoRefillField);
  const addProvider = useSettingsStore((state) => state.addRegisterProvider);
  const updateProvider = useSettingsStore((state) => state.updateRegisterProvider);
  const deleteProvider = useSettingsStore((state) => state.deleteRegisterProvider);
  const save = useSettingsStore((state) => state.saveRegister);
  const toggle = useSettingsStore((state) => state.toggleRegister);
  const reset = useSettingsStore((state) => state.resetRegister);
  const repairAbnormal = useSettingsStore((state) => state.repairAbnormalRegisterAccounts);
  const testOutlookPool = useSettingsStore((state) => state.testOutlookPool);

  const [configOpen, setConfigOpen] = useState(false);
  const configSectionRef = useRef<HTMLElement | null>(null);
  const logViewportRef = useRef<HTMLDivElement | null>(null);
  const [yydsDomainBlacklistText, setYydsDomainBlacklistText] = useState("");
  const [isYydsDomainBlacklistLoading, setIsYydsDomainBlacklistLoading] = useState(false);
  const logs = config?.logs || [];
  const hasYYDSProvider = Boolean(
    config?.mail?.providers?.some((provider) => String((provider as Record<string, unknown>).type || "") === "yyds_mail"),
  );

  // 展开后把整个配置面板的底部滚到视口里。等一帧让 DOM 先渲染完，
  // 否则 scrollIntoView 拿到的是没展开前的位置。
  useEffect(() => {
    if (!configOpen) return;
    const raf = requestAnimationFrame(() => {
      configSectionRef.current?.scrollIntoView({ block: "end", behavior: "smooth" });
    });
    return () => cancelAnimationFrame(raf);
  }, [configOpen]);

  useEffect(() => {
    const viewport = logViewportRef.current;
    if (!viewport) return;
    viewport.scrollTop = viewport.scrollHeight;
  }, [logs.length]);

  useEffect(() => {
    if (!hasYYDSProvider) return;
    let cancelled = false;
    setIsYydsDomainBlacklistLoading(true);
    fetchYYDSDomainBlacklist()
      .then((data) => {
        if (!cancelled) setYydsDomainBlacklistText((data.items || []).join("\n"));
      })
      .catch(() => {
        if (!cancelled) setYydsDomainBlacklistText("");
      })
      .finally(() => {
        if (!cancelled) setIsYydsDomainBlacklistLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [hasYYDSProvider]);

  if (isLoading) {
    return (
      <div className="flex items-center justify-center rounded-xl border border-border bg-card p-10">
        <LoaderCircle className="size-5 animate-spin text-muted-foreground" />
      </div>
    );
  }

  if (!config) return null;

  const stats = config.stats || { success: 0, fail: 0, done: 0, running: 0, threads: config.threads };
  const providers = config.mail.providers || [];
  const isRepairingAbnormal = config.enabled && stats.job_kind === "repair_abnormal";

  const targetTotal =
    config.mode === "quota"
      ? Number(config.target_quota || 0)
      : config.mode === "available"
        ? Number(config.target_available || 0)
        : Number(config.total || 0);
  const currentValue =
    config.mode === "quota"
      ? Number(stats.current_quota || 0)
      : config.mode === "available"
        ? Number(stats.current_available || 0)
        : Number(stats.success || 0);
  const progress = targetTotal > 0 ? Math.min(100, Math.round((currentValue / targetTotal) * 100)) : 0;
  const modeLabel = config.mode === "quota" ? "额度" : config.mode === "available" ? "可用账号" : "已注册";

  const kpis: { label: string; value: string | number; tone?: "ok" | "warn" | "error" | "muted" }[] = [
    { label: "成功", value: stats.success, tone: "ok" },
    { label: "失败", value: stats.fail, tone: stats.fail > 0 ? "error" : "muted" },
    { label: "完成", value: stats.done },
    { label: "线程", value: `${stats.running}/${stats.threads}` },
    { label: "平均", value: `${stats.avg_seconds || 0}s` },
    { label: "已运行", value: `${stats.elapsed_seconds || 0}s` },
    { label: "成功率", value: `${stats.success_rate || 0}%`, tone: (stats.success_rate || 0) >= 80 ? "ok" : "warn" },
    { label: "额度", value: stats.current_quota || 0, tone: "muted" },
  ];
  const updateProviderType = (index: number, type: string) => {
    updateProvider(index, {
      type,
      enable: true,
      ...(type === "cloudflare_temp_email" ? { api_base: "", admin_password: "", domain: [] } : {}),
      ...(type === "tempmail_lol" ? { api_key: "", domain: [] } : {}),
      ...(type === "moemail" ? { api_base: "", api_key: "", domain: [] } : {}),
      ...(type === "inbucket" ? { api_base: "", domain: [], random_subdomain: true } : {}),
      ...(type === "duckmail" ? { api_key: "", default_domain: "duckmail.sbs" } : {}),
      ...(type === "gptmail" ? { api_key: "", default_domain: "" } : {}),
      ...(type === "yyds_mail" ? { api_base: "https://maliapi.215.im/v1", api_key: "", domain: [], subdomain: "", wildcard: false } : {}),
      ...(type === "cloudmail" ? { api_base: "", admin_email: "", admin_password: "", domain: [] } : {}),
      ...(type === "outlook_token" ? { mailboxes: "", mode: "auto", imap_host: "outlook.office365.com" } : {}),
    });
  };

  const saveYYDSDomainBlacklist = async () => {
    const domains = yydsDomainBlacklistText
      .split(/[\n,]/)
      .map((item) => item.trim().toLowerCase().replace(/^@+/, ""))
      .filter(Boolean);
    setIsYydsDomainBlacklistLoading(true);
    try {
      const data = await replaceYYDSDomainBlacklist(domains);
      setYydsDomainBlacklistText((data.items || []).join("\n"));
      toast.success("yyds 禁用域名已保存");
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "保存 yyds 禁用域名失败");
    } finally {
      setIsYydsDomainBlacklistLoading(false);
    }
  };

  const clearYYDSDomainBlacklist = async () => {
    setIsYydsDomainBlacklistLoading(true);
    try {
      const data = await resetYYDSDomainBlacklist();
      setYydsDomainBlacklistText((data.items || []).join("\n"));
      toast.success("yyds 禁用域名已清空");
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "清空 yyds 禁用域名失败");
    } finally {
      setIsYydsDomainBlacklistLoading(false);
    }
  };

  return (
    <div className="space-y-3">
      <section className="overflow-hidden rounded-xl border border-border bg-card shadow-sm">
        <div className="flex flex-col gap-4 border-b border-border bg-gradient-to-br from-card to-secondary/40 p-5 sm:flex-row sm:items-center sm:justify-between">
          <div className="flex items-center gap-3">
            <span
              className={cn(
                "relative grid size-10 place-items-center rounded-lg border",
                config.enabled
                  ? "border-emerald-200 bg-emerald-50 text-emerald-700"
                  : "border-border bg-secondary text-muted-foreground",
              )}
            >
              {config.enabled ? (
                <>
                  <span className="absolute inset-0 animate-ping rounded-lg bg-emerald-400/20" />
                  <span className="relative size-2 rounded-full bg-emerald-500" />
                </>
              ) : (
                <span className="size-2 rounded-full bg-muted-foreground/50" />
              )}
            </span>
            <div className="space-y-1">
              <div className="flex items-center gap-2">
                <span
                  className={cn(
                    "font-data text-[10px] font-bold tracking-[0.22em] uppercase",
                    config.enabled ? "text-emerald-600" : "text-muted-foreground",
                  )}
                >
                  {config.enabled ? "运行中" : "空闲"}
                </span>
                <span className="h-px w-6 bg-border" />
                <span className="font-data text-[10px] font-medium tracking-wider text-muted-foreground uppercase">
                  模式 · {config.mode === "total" ? "总数" : config.mode === "quota" ? "额度" : "可用"}
                </span>
              </div>
              <div className="flex items-baseline gap-2">
                <span className="font-data tabular-nums text-[28px] font-semibold leading-none tracking-tight text-foreground">
                  {currentValue}
                </span>
                <span className="font-data tabular-nums text-[16px] font-medium text-muted-foreground">
                  / {targetTotal || "∞"}
                </span>
                <span className="ml-1 font-data text-[11px] font-medium text-muted-foreground">{modeLabel}</span>
              </div>
            </div>
          </div>
          <div className="flex items-center gap-2">
            <Button
              className={cn(
                "h-10 cursor-pointer rounded-lg px-5 text-[13px] font-medium transition",
                config.enabled
                  ? "bg-rose-500 text-white shadow-sm shadow-rose-500/30 hover:bg-rose-600"
                  : "bg-foreground text-background shadow-sm hover:bg-foreground/90",
              )}
              onClick={() => void toggle()}
              disabled={isSaving}
            >
              {isSaving ? (
                <LoaderCircle className="size-4 animate-spin" />
              ) : config.enabled ? (
                <Square className="size-4 fill-current" />
              ) : (
                <Play className="size-4 fill-current" />
              )}
              {config.enabled ? "停止" : "启动"}
            </Button>
            <Button
              variant="outline"
              className={cn(
                "h-10 cursor-pointer rounded-lg border-border px-3",
                isRepairingAbnormal
                  ? "border-rose-200 bg-rose-50 text-rose-600 hover:bg-rose-100"
                  : "bg-background text-foreground",
              )}
              onClick={() => void repairAbnormal()}
              disabled={isSaving || (config.enabled && !isRepairingAbnormal)}
              title={isRepairingAbnormal ? "停止修复" : "修复异常账号"}
            >
              {isSaving ? (
                <LoaderCircle className="size-4 animate-spin" />
              ) : isRepairingAbnormal ? (
                <Square className="size-4 fill-current" />
              ) : (
                <Wrench className="size-4" />
              )}
              {isRepairingAbnormal ? "停止修复" : "修复异常账号"}
            </Button>
            <Button
              variant="outline"
              className="h-10 cursor-pointer rounded-lg border-border bg-background px-3 text-foreground"
              onClick={() => void reset()}
              disabled={isSaving || config.enabled}
              title="重置"
            >
              <RotateCcw className="size-4" />
            </Button>
          </div>
        </div>

        <div className="px-5 pt-4">
          <div className="relative h-1.5 overflow-hidden rounded-full bg-secondary">
            <div
              className={cn(
                "absolute inset-y-0 left-0 rounded-full transition-all duration-500",
                config.enabled
                  ? "bg-gradient-to-r from-emerald-400 to-emerald-500"
                  : "bg-gradient-to-r from-muted-foreground/40 to-muted-foreground/60",
              )}
              style={{ width: `${progress}%` }}
            />
          </div>
          <div className="mt-1.5 flex items-center justify-between font-data text-[10px] font-medium tracking-wider text-muted-foreground uppercase">
            <span>进度</span>
            <span className="tabular-nums">{progress}%</span>
          </div>
        </div>

        <div className="grid grid-cols-2 gap-px overflow-hidden bg-border md:grid-cols-4 lg:grid-cols-8">
          {kpis.map((kpi) => (
            <div key={kpi.label} className="flex flex-col gap-1 bg-card px-4 py-3">
              <span className="font-data text-[10px] font-semibold tracking-[0.16em] text-muted-foreground uppercase">
                {kpi.label}
              </span>
              <span
                className={cn(
                  "font-data tabular-nums text-[18px] font-semibold leading-tight",
                  kpi.tone === "ok" && "text-emerald-600",
                  kpi.tone === "error" && "text-rose-500",
                  kpi.tone === "warn" && "text-amber-600",
                  kpi.tone === "muted" && "text-muted-foreground",
                  !kpi.tone && "text-foreground",
                )}
              >
                {kpi.value}
              </span>
            </div>
          ))}
        </div>
      </section>

      {!config.enabled ? (
        <div className="flex items-center gap-2 rounded-lg border border-amber-200 bg-amber-50 px-3 py-2 text-xs text-amber-800">
          <AlertTriangle className="size-4 shrink-0" />
          <span>启动前先保存配置。配置面板在最下方，支持折叠展开。</span>
        </div>
      ) : null}

      <section className="overflow-hidden rounded-xl border border-border bg-[oklch(0.18_0.018_260)] shadow-sm">
        <div className="flex items-center justify-between border-b border-white/10 bg-black/20 px-4 py-2.5">
          <div className="flex items-center gap-2">
            <Terminal className="size-3.5 text-emerald-400" />
            <span className="font-data text-[10px] font-old tracking-[0.22em] text-white/70 uppercase">
              Live Console
            </span>
            <span className="ml-1 font-data text-[10px] tabular-nums text-white/40">{logs.length} lines</span>
          </div>
          <div className="flex items-center gap-1.5">
            <span className="size-2 rounded-full bg-rose-400/80" />
            <span className="size-2 rounded-full bg-amber-400/80" />
            <span className="size-2 rounded-full bg-emerald-400/80" />
          </div>
        </div>
        <div ref={logViewportRef} className="h-[calc(100vh-540px)] min-h-[280px] overflow-y-auto px-4 py-3 font-data text-[12px] leading-[1.65]">
          {logs.length === 0 ? (
            <div className="flex h-full items-center justify-center text-white/30">
              <span>$ waiting for events...</span>
            </div>
          ) : (
            logs.map((item, index) => (
                <div
                  key={`${item.time}-${index}`}
                  className={cn(
                    "flex gap-3 transition hover:bg-white/5",
                    item.level === "red" && "text-rose-300",
                    item.level === "green" && "text-emerald-300",
                    item.level === "yellow" && "text-amber-300",
                    !item.level || item.level === "info" ? "text-white/80" : "",
                  )}
                >
                  <span className="shrink-0 text-white/30">{new Date(item.time).toLocaleTimeString()}</span>
                  <span className="min-w-0 break-words">{item.text}</span>
                </div>
              ))
          )}
        </div>
      </section>

      <section
        ref={configSectionRef}
        className="overflow-hidden rounded-xl border border-border bg-card shadow-sm"
      >
        <div
          role="button"
          tabIndex={0}
          aria-expanded={configOpen}
          className="flex w-full cursor-pointer items-center justify-between gap-3 px-5 py-3.5 transition hover:bg-secondary/40 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
          onClick={() => setConfigOpen((prev) => !prev)}
          onKeyDown={(event) => {
            if (event.key === "Enter" || event.key === " ") {
              event.preventDefault();
              setConfigOpen((prev) => !prev);
            }
          }}
        >
          <div className="flex items-center gap-3">
            <span className="grid size-8 place-items-center rounded-lg border border-border bg-secondary">
              <Settings2 className="size-4 text-foreground" />
            </span>
            <div className="text-left">
              <div className="font-data text-[10px] font-semibold tracking-[0.18em] text-muted-foreground uppercase">
                Configuration
              </div>
              <div className="text-[14px] font-semibold text-foreground">注册配置 · {providers.length} provider</div>
            </div>
          </div>
          <div className="flex items-center gap-2">
            <Button
              variant="outline"
              className="h-9 cursor-pointer rounded-lg border-border bg-background px-3 text-[12px] text-foreground"
              onClick={(event) => {
                event.stopPropagation();
                void save();
              }}
              disabled={isSaving || config.enabled}
            >
              {isSaving ? <LoaderCircle className="size-4 animate-spin" /> : <Save className="size-4" />}
              保存
            </Button>
            {configOpen ? (
              <ChevronDown className="size-4 text-muted-foreground" />
            ) : (
              <ChevronRight className="size-4 text-muted-foreground" />
            )}
          </div>
        </div>
        {configOpen ? (
        <section className="space-y-4 border-t border-border p-5">
          <div className="flex items-start justify-between gap-3">
            <div className="flex items-center gap-3">
              <div className="grid size-9 place-items-center rounded-lg border border-border bg-secondary">
                <UserPlus className="size-4 text-foreground" />
              </div>
              <div>
                <div className="font-data text-[10px] font-semibold tracking-[0.18em] text-muted-foreground uppercase">Configuration</div>
                <h2 className="text-[15px] font-semibold tracking-tight text-foreground">注册配置</h2>
              </div>
            </div>
            <Button className="h-9 cursor-pointer rounded-lg bg-foreground px-4 text-background hover:bg-foreground/90" onClick={() => void save()} disabled={isSaving || config.enabled}>
              {isSaving ? <LoaderCircle className="size-4 animate-spin" /> : <Save className="size-4" />}
              保存配置
            </Button>
          </div>

          <div className="grid gap-3 md:grid-cols-3">
            <div className="space-y-1.5">
              <label className="font-data text-[10px] font-semibold tracking-[0.16em] text-muted-foreground uppercase">注册模式</label>
              <Select value={config.mode || "total"} onValueChange={(value) => setMode(value as "total" | "quota" | "available")} disabled={config.enabled}>
                <SelectTrigger className="h-10 rounded-lg border-border bg-background">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="total">注册总数</SelectItem>
                  <SelectItem value="quota">号池剩余额度</SelectItem>
                  <SelectItem value="available">可用账号数量</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div className="space-y-1.5">
              <label className="font-data text-[10px] font-semibold tracking-[0.16em] text-muted-foreground uppercase">注册总数</label>
              <Input value={String(config.total)} onChange={(event) => setTotal(event.target.value)} className="h-10 rounded-lg border-border bg-background font-data tabular-nums" disabled={config.enabled || config.mode !== "total"} />
            </div>
            <div className="space-y-1.5">
              <label className="font-data text-[10px] font-semibold tracking-[0.16em] text-muted-foreground uppercase">线程数</label>
              <Input value={String(config.threads)} onChange={(event) => setThreads(event.target.value)} className="h-10 rounded-lg border-border bg-background font-data tabular-nums" disabled={config.enabled} />
            </div>
            <div className="space-y-1.5">
              <label className="font-data text-[10px] font-semibold tracking-[0.16em] text-muted-foreground uppercase">注册代理</label>
              <Input value={config.proxy} onChange={(event) => setProxy(event.target.value)} placeholder="http://127.0.0.1:7890" className="h-10 rounded-lg border-border bg-background font-data text-[13px]" disabled={config.enabled} />
            </div>
            <div className="space-y-1.5">
              <label className="font-data text-[10px] font-semibold tracking-[0.16em] text-muted-foreground uppercase">目标剩余额度</label>
              <Input value={String(config.target_quota || "")} onChange={(event) => setTargetQuota(event.target.value)} className="h-10 rounded-lg border-border bg-background font-data tabular-nums" disabled={config.enabled || config.mode !== "quota"} />
            </div>
            <div className="space-y-1.5">
              <label className="font-data text-[10px] font-semibold tracking-[0.16em] text-muted-foreground uppercase">目标可用账号</label>
              <Input value={String(config.target_available || "")} onChange={(event) => setTargetAvailable(event.target.value)} className="h-10 rounded-lg border-border bg-background font-data tabular-nums" disabled={config.enabled || config.mode !== "available"} />
            </div>
            <div className="space-y-1.5">
              <label className="font-data text-[10px] font-semibold tracking-[0.16em] text-muted-foreground uppercase">检查间隔（秒）</label>
              <Input value={String(config.check_interval || "")} onChange={(event) => setCheckInterval(event.target.value)} className="h-10 rounded-lg border-border bg-background font-data tabular-nums" disabled={config.enabled || config.mode === "total"} />
            </div>
            <div className="space-y-1.5">
              <label className="font-data text-[10px] font-semibold tracking-[0.16em] text-muted-foreground uppercase">指定账号密码</label>
              <Input type="password" value={String(config.fixed_password || "")} onChange={(event) => setFixedPassword(event.target.value)} placeholder="留空=随机生成" className="h-10 rounded-lg border-border bg-background font-data text-[13px]" disabled={config.enabled} autoComplete="new-password" />
            </div>
            <div className="space-y-1.5">
              <label className="font-data text-[10px] font-semibold tracking-[0.16em] text-muted-foreground uppercase">单任务超时（秒）</label>
              <Input value={String(config.task_timeout_seconds || "")} onChange={(event) => setTaskTimeout(event.target.value)} className="h-10 rounded-lg border-border bg-background font-data tabular-nums" disabled={config.enabled} />
            </div>
            <div className="space-y-1.5">
              <label className="font-data text-[10px] font-semibold tracking-[0.16em] text-muted-foreground uppercase">无进度超时（秒）</label>
              <Input value={String(config.task_stall_timeout_seconds || "")} onChange={(event) => setTaskStallTimeout(event.target.value)} className="h-10 rounded-lg border-border bg-background font-data tabular-nums" disabled={config.enabled} />
            </div>
            <label className="flex items-center gap-2.5 pt-7 text-sm text-foreground">
              <Checkbox checked={Boolean(config.auto_refill?.enabled)} onCheckedChange={(checked) => setAutoRefillField("enabled", Boolean(checked))} disabled={config.enabled} />
              <span>启用自动补号</span>
            </label>
            <div className="space-y-1.5">
              <label className="font-data text-[10px] font-semibold tracking-[0.16em] text-muted-foreground uppercase">补号阈值</label>
              <Input value={String(config.auto_refill?.min_available || "")} onChange={(event) => setAutoRefillField("min_available", event.target.value)} className="h-10 rounded-lg border-border bg-background font-data tabular-nums" disabled={config.enabled || !config.auto_refill?.enabled} />
            </div>
            <div className="space-y-1.5">
              <label className="font-data text-[10px] font-semibold tracking-[0.16em] text-muted-foreground uppercase">每轮补号数量</label>
              <Input value={String(config.auto_refill?.batch_total || "")} onChange={(event) => setAutoRefillField("batch_total", event.target.value)} className="h-10 rounded-lg border-border bg-background font-data tabular-nums" disabled={config.enabled || !config.auto_refill?.enabled} />
            </div>
            <div className="space-y-1.5">
              <label className="font-data text-[10px] font-semibold tracking-[0.16em] text-muted-foreground uppercase">补号检查间隔</label>
              <Input value={String(config.auto_refill?.check_interval || "")} onChange={(event) => setAutoRefillField("check_interval", event.target.value)} className="h-10 rounded-lg border-border bg-background font-data tabular-nums" disabled={config.enabled || !config.auto_refill?.enabled} />
            </div>
          </div>

          <div className="space-y-3 border-t border-border pt-4">
            <div className="flex items-center justify-between gap-3">
              <div>
                <div className="font-data text-[10px] font-semibold tracking-[0.18em] text-muted-foreground uppercase">Mail Providers</div>
                <h3 className="text-[14px] font-semibold text-foreground">邮箱配置</h3>
              </div>
              <div className="flex items-center gap-2">
                <Button type="button" variant="outline" className="h-9 cursor-pointer rounded-lg border-border bg-background px-3 text-foreground" onClick={() => void testOutlookPool()} disabled={isSaving}>
                  {isSaving ? <LoaderCircle className="size-4 animate-spin" /> : <MailCheck className="size-4" />}
                  Outlook 检测
                </Button>
                <Button type="button" variant="outline" className="h-9 cursor-pointer rounded-lg border-border bg-background px-3 text-foreground" onClick={addProvider} disabled={config.enabled}>
                  <Plus className="size-4" />
                  添加
                </Button>
              </div>
            </div>

            <div className="grid gap-3 md:grid-cols-3">
              <div className="space-y-1.5">
                <label className="font-data text-[10px] font-semibold tracking-[0.16em] text-muted-foreground uppercase">请求超时</label>
                <Input value={String(config.mail.request_timeout || "")} onChange={(event) => setMailField("request_timeout", event.target.value)} className="h-10 rounded-lg border-border bg-background font-data tabular-nums" disabled={config.enabled} />
              </div>
              <div className="space-y-1.5">
                <label className="font-data text-[10px] font-semibold tracking-[0.16em] text-muted-foreground uppercase">等待验证码超时</label>
                <Input value={String(config.mail.wait_timeout || "")} onChange={(event) => setMailField("wait_timeout", event.target.value)} className="h-10 rounded-lg border-border bg-background font-data tabular-nums" disabled={config.enabled} />
              </div>
              <div className="space-y-1.5">
                <label className="font-data text-[10px] font-semibold tracking-[0.16em] text-muted-foreground uppercase">轮询间隔</label>
                <Input value={String(config.mail.wait_interval || "")} onChange={(event) => setMailField("wait_interval", event.target.value)} className="h-10 rounded-lg border-border bg-background font-data tabular-nums" disabled={config.enabled} />
              </div>
            </div>
            <label className="flex items-center gap-2.5 text-sm text-foreground">
              <Checkbox checked={Boolean(config.mail.api_use_register_proxy ?? true)} onCheckedChange={(checked) => setMailUseRegisterProxy(Boolean(checked))} disabled={config.enabled} />
              <span>邮箱 API 请求使用注册代理</span>
            </label>

            <div className="space-y-3">
              {providers.map((provider, index) => {
                const type = String(provider.type || "tempmail_lol");
                const domains = Array.isArray(provider.domain) ? provider.domain.map(String).join("\n") : "";
                const yydsManualDomainBlacklist = Array.isArray(provider.domain_blacklist) ? provider.domain_blacklist.map(String).join("\n") : "";
                const outlookMailboxes = Array.isArray(provider.mailboxes) ? provider.mailboxes.map(String).join("\n") : String(provider.mailboxes || "");
                return (
                  <div key={index} className="space-y-3 rounded-lg border border-border bg-secondary/30 p-3">
                    <div className="flex items-center justify-between gap-3">
                      <label className="flex items-center gap-2.5 text-sm text-foreground">
                        <Checkbox checked={Boolean(provider.enable)} onCheckedChange={(checked) => updateProvider(index, { enable: Boolean(checked) })} disabled={config.enabled} />
                        <span>启用</span>
                      </label>
                      <button type="button" className="cursor-pointer rounded-md p-1.5 text-muted-foreground transition hover:bg-rose-50 hover:text-rose-500 disabled:opacity-50" onClick={() => deleteProvider(index)} disabled={config.enabled || providers.length <= 1} title="删除 provider">
                        <Trash2 className="size-4" />
                      </button>
                    </div>

                    <div className="grid gap-3 md:grid-cols-2">
                      <div className="space-y-1.5">
                        <label className="font-data text-[10px] font-semibold tracking-[0.16em] text-muted-foreground uppercase">类型</label>
                        <Select value={type} onValueChange={(value) => updateProviderType(index, value)} disabled={config.enabled}>
                          <SelectTrigger className="h-10 rounded-lg border-border bg-background">
                            <SelectValue />
                          </SelectTrigger>
                          <SelectContent>
                            <SelectItem value="cloudflare_temp_email">cloudflare_temp_email</SelectItem>
                            <SelectItem value="tempmail_lol">tempmail_lol</SelectItem>
                            <SelectItem value="moemail">moemail</SelectItem>
                            <SelectItem value="inbucket">inbucket_mail</SelectItem>
                            <SelectItem value="duckmail">duckmail</SelectItem>
                            <SelectItem value="gptmail">gptmail(未测试)</SelectItem>
                            <SelectItem value="yyds_mail">yyds_mail</SelectItem>
                            <SelectItem value="cloudmail">cloudmail</SelectItem>
                            <SelectItem value="outlook_token">outlook_token</SelectItem>
                          </SelectContent>
                        </Select>
                      </div>
                      {type === "cloudflare_temp_email" || type === "moemail" || type === "inbucket" || type === "yyds_mail" || type === "cloudmail" ? (
                        <>
                          <div className="space-y-1.5">
                            <label className="font-data text-[10px] font-semibold tracking-[0.16em] text-muted-foreground uppercase">API Base</label>
                            <Input value={String(provider.api_base || "")} onChange={(event) => updateProvider(index, { api_base: event.target.value })} placeholder={type === "cloudmail" ? "https://your-cloudmail.com/api" : ""} className="h-10 rounded-lg border-border bg-background font-data text-[13px]" disabled={config.enabled} />
                          </div>
                          {type === "cloudflare_temp_email" ? (
                            <div className="space-y-1.5">
                              <label className="font-data text-[10px] font-semibold tracking-[0.16em] text-muted-foreground uppercase">Admin Password</label>
                              <Input value={String(provider.admin_password || "")} onChange={(event) => updateProvider(index, { admin_password: event.target.value })} className="h-10 rounded-lg border-border bg-background font-data text-[13px]" disabled={config.enabled} />
                            </div>
                          ) : null}
                          {type === "cloudmail" ? (
                            <>
                              <div className="space-y-1.5">
                                <label className="font-data text-[10px] font-semibold tracking-[0.16em] text-muted-foreground uppercase">Admin Email</label>
                                <Input value={String(provider.admin_email || "")} onChange={(event) => updateProvider(index, { admin_email: event.target.value })} placeholder="admin@example.com" className="h-10 rounded-lg border-border bg-background font-data text-[13px]" disabled={config.enabled} />
                              </div>
                              <div className="space-y-1.5">
                                <label className="font-data text-[10px] font-semibold tracking-[0.16em] text-muted-foreground uppercase">Admin Password</label>
                                <Input type="password" value={String(provider.admin_password || "")} onChange={(event) => updateProvider(index, { admin_password: event.target.value })} className="h-10 rounded-lg border-border bg-background font-data text-[13px]" disabled={config.enabled} />
                              </div>
                            </>
                          ) : null}
                        </>
                      ) : null}
                      {type === "inbucket" ? (
                        <label className="flex items-center gap-2.5 pt-7 text-sm text-foreground">
                          <Checkbox checked={Boolean(provider.random_subdomain ?? true)} onCheckedChange={(checked) => updateProvider(index, { random_subdomain: Boolean(checked) })} disabled={config.enabled} />
                          <span>启用随机子域名</span>
                        </label>
                      ) : null}
                      {type === "tempmail_lol" || type === "moemail" || type === "duckmail" || type === "gptmail" || type === "yyds_mail" ? (
                        <div className="space-y-1.5">
                          <label className="font-data text-[10px] font-semibold tracking-[0.16em] text-muted-foreground uppercase">API Key</label>
                          <Input value={String(provider.api_key || "")} onChange={(event) => updateProvider(index, { api_key: event.target.value })} className="h-10 rounded-lg border-border bg-background font-data text-[13px]" disabled={config.enabled} />
                        </div>
                      ) : null}
                      {type === "duckmail" || type === "gptmail" ? (
                        <div className="space-y-1.5">
                          <label className="font-data text-[10px] font-semibold tracking-[0.16em] text-muted-foreground uppercase">Default Domain</label>
                          <Input value={String(provider.default_domain || "")} onChange={(event) => updateProvider(index, { default_domain: event.target.value })} placeholder={type === "duckmail" ? "duckmail.sbs" : ""} className="h-10 rounded-lg border-border bg-background font-data text-[13px]" disabled={config.enabled} />
                        </div>
                      ) : null}
                      {type === "yyds_mail" ? (
                        <>
                          <div className="space-y-1.5">
                            <label className="font-data text-[10px] font-semibold tracking-[0.16em] text-muted-foreground uppercase">Subdomain</label>
                            <Input value={String(provider.subdomain || "")} onChange={(event) => updateProvider(index, { subdomain: event.target.value })} className="h-10 rounded-lg border-border bg-background font-data text-[13px]" disabled={config.enabled} />
                          </div>
                          <label className="flex items-center gap-2.5 pt-7 text-sm text-foreground">
                            <Checkbox checked={Boolean(provider.wildcard)} onCheckedChange={(checked) => updateProvider(index, { wildcard: Boolean(checked) })} disabled={config.enabled} />
                            <span>Wildcard</span>
                          </label>
                        </>
                      ) : null}
                      {type === "outlook_token" ? (
                        <>
                          <div className="space-y-1.5">
                            <label className="font-data text-[10px] font-semibold tracking-[0.16em] text-muted-foreground uppercase">读取模式</label>
                            <Select value={String(provider.mode || "auto")} onValueChange={(value) => updateProvider(index, { mode: value })} disabled={config.enabled}>
                              <SelectTrigger className="h-10 rounded-lg border-border bg-background">
                                <SelectValue />
                              </SelectTrigger>
                              <SelectContent>
                                <SelectItem value="auto">auto</SelectItem>
                                <SelectItem value="graph">graph</SelectItem>
                                <SelectItem value="imap">imap</SelectItem>
                              </SelectContent>
                            </Select>
                          </div>
                          <div className="space-y-1.5">
                            <label className="font-data text-[10px] font-semibold tracking-[0.16em] text-muted-foreground uppercase">IMAP Host</label>
                            <Input value={String(provider.imap_host || "outlook.office365.com")} onChange={(event) => updateProvider(index, { imap_host: event.target.value })} className="h-10 rounded-lg border-border bg-background font-data text-[13px]" disabled={config.enabled || String(provider.mode || "auto") === "graph"} />
                          </div>
                        </>
                      ) : null}
                    </div>

                    {type === "tempmail_lol" || type === "cloudflare_temp_email" || type === "moemail" || type === "inbucket" || type === "yyds_mail" || type === "cloudmail" ? (
                      <div className="space-y-1.5">
                        <label className="font-data text-[10px] font-semibold tracking-[0.16em] text-muted-foreground uppercase">{type === "inbucket" ? "基础域名列表" : "Domain"}</label>
                        <Textarea value={domains} onChange={(event) => updateProvider(index, { domain: event.target.value.split(/[\n,]/).map((item) => item.trim()).filter(Boolean) })} placeholder={type === "inbucket" ? "每行一个基础域名，系统会自动生成随机子域名" : type === "moemail" ? "每行一个域名" : type === "cloudmail" ? "每行一个域名，支持多域名轮询，可加 @ 前缀" : "每行一个域名，留空则使用服务默认域名"} className="min-h-20 rounded-lg border-border bg-background font-data text-[12px]" disabled={config.enabled} />
                      </div>
                    ) : null}
                    {type === "yyds_mail" ? (
                      <div className="space-y-2 rounded-lg border border-amber-200 bg-amber-50/60 p-3">
                        <div className="space-y-1.5">
                          <label className="font-data text-[10px] font-semibold tracking-[0.16em] text-amber-700 uppercase">手动禁用域名后缀</label>
                          <Textarea
                            value={yydsManualDomainBlacklist}
                            onChange={(event) => updateProvider(index, { domain_blacklist: event.target.value.split(/[\n,]/).map((item) => item.trim()).filter(Boolean) })}
                            placeholder="每行一个域名后缀，会和自动黑名单一起跳过"
                            className="min-h-16 rounded-lg border-amber-200 bg-white font-data text-[12px]"
                            disabled={config.enabled}
                          />
                        </div>
                        <div className="flex items-center justify-between gap-3">
                          <div>
                            <label className="font-data text-[10px] font-semibold tracking-[0.16em] text-amber-700 uppercase">YYDS 禁用域名后缀</label>
                            <div className="mt-1 text-xs leading-5 text-amber-700/80">
                              注册阶段返回 user_register_http_400 后会自动加入这里，后续生成邮箱会跳过这些域名。
                            </div>
                          </div>
                          {isYydsDomainBlacklistLoading ? <LoaderCircle className="size-4 animate-spin text-amber-700" /> : null}
                        </div>
                        <Textarea
                          value={yydsDomainBlacklistText}
                          onChange={(event) => setYydsDomainBlacklistText(event.target.value)}
                          placeholder="每行一个域名后缀，例如 example.com"
                          className="min-h-20 rounded-lg border-amber-200 bg-white font-data text-[12px]"
                          disabled={config.enabled || isYydsDomainBlacklistLoading}
                        />
                        <div className="flex items-center gap-2">
                          <Button type="button" variant="outline" className="h-8 cursor-pointer rounded-lg border-amber-200 bg-white px-3 text-xs text-amber-800" onClick={() => void saveYYDSDomainBlacklist()} disabled={config.enabled || isYydsDomainBlacklistLoading}>
                            保存禁用域名
                          </Button>
                          <Button type="button" variant="outline" className="h-8 cursor-pointer rounded-lg border-border bg-white px-3 text-xs text-muted-foreground" onClick={() => void clearYYDSDomainBlacklist()} disabled={config.enabled || isYydsDomainBlacklistLoading || !yydsDomainBlacklistText.trim()}>
                            清空
                          </Button>
                        </div>
                      </div>
                    ) : null}
                    {type === "outlook_token" ? (
                      <div className="space-y-1.5">
                        <label className="font-data text-[10px] font-semibold tracking-[0.16em] text-muted-foreground uppercase">Outlook 邮箱 Token</label>
                        <Textarea value={outlookMailboxes} onChange={(event) => updateProvider(index, { mailboxes: event.target.value })} placeholder="email----password----client_id----refresh_token&#10;留空保存不会清空已导入邮箱池" className="min-h-28 rounded-lg border-border bg-background font-data text-[12px]" disabled={config.enabled} />
                        {Array.isArray(provider.mailboxes_preview) && provider.mailboxes_preview.length > 0 ? (
                          <div className="font-data text-[11px] text-muted-foreground">已导入 {Number(provider.mailboxes_count || provider.mailboxes_preview.length)} 个：{provider.mailboxes_preview.map(String).slice(0, 6).join("、")}</div>
                        ) : null}
                      </div>
                    ) : null}
                  </div>
                );
              })}
            </div>
          </div>

      </section>
        ) : null}
      </section>
    </div>
  );
}
