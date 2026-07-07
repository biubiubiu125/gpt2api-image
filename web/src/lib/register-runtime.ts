import type { RegisterConfig } from "@/lib/api";

export type RegisterRuntimeState = {
  lifecycle: string;
  isRunning: boolean;
  isStopping: boolean;
  isActive: boolean;
  isRepairing: boolean;
};

export function getRegisterRuntimeState(config: RegisterConfig | null | undefined): RegisterRuntimeState {
  const lifecycle = String(config?.stats?.lifecycle || config?.lifecycle || "").trim().toLowerCase();
  const runningCount = Number(config?.stats?.running || 0);
  const isRunningFlag = Boolean(config?.stats?.is_running ?? config?.is_running);
  const isStoppingFlag = Boolean(config?.stats?.is_stopping ?? config?.is_stopping);
  const isStopping = isStoppingFlag || lifecycle === "stopping" || Boolean(config && !config.enabled && runningCount > 0);
  const isRepairing = lifecycle === "repairing" || Boolean(config?.enabled && config?.stats?.job_kind === "repair_abnormal");
  const isRunning = isRunningFlag || lifecycle === "running" || isRepairing || Boolean(config?.enabled);
  const normalizedLifecycle = lifecycle || (isStopping ? "stopping" : isRunning ? "running" : "idle");

  return {
    lifecycle: normalizedLifecycle,
    isRunning,
    isStopping,
    isActive: isRunning || isStopping,
    isRepairing,
  };
}
