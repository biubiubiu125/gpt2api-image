"use client";

import { LoaderCircle } from "lucide-react";
import { useEffect, useRef } from "react";

import webConfig from "@/constants/common-env";
import { useAuthGuard } from "@/lib/use-auth-guard";
import { getStoredAuthKey } from "@/store/auth";

import { useSettingsStore } from "../settings/store";
import { RegisterCard } from "./components/register-card";

function RegisterDataController() {
  const didLoadRef = useRef(false);
  const loadRegister = useSettingsStore((state) => state.loadRegister);
  const setRegisterConfig = useSettingsStore((state) => state.setRegisterConfig);

  useEffect(() => {
    if (didLoadRef.current) return;
    didLoadRef.current = true;
    void loadRegister();
  }, [loadRegister]);

  useEffect(() => {
    const controller = new AbortController();
    let fallbackTimer: ReturnType<typeof setTimeout> | null = null;

    const readEvents = async () => {
      try {
        const authKey = await getStoredAuthKey();
        const response = await fetch(`${webConfig.apiUrl.replace(/\/$/, "")}/api/register/events`, {
          headers: authKey ? { Authorization: `Bearer ${authKey}` } : {},
          signal: controller.signal,
        });
        if (!response.ok || !response.body) {
          throw new Error(`register events ${response.status}`);
        }
        const reader = response.body.getReader();
        const decoder = new TextDecoder();
        let buffer = "";
        while (!controller.signal.aborted) {
          const { value, done } = await reader.read();
          if (done) break;
          buffer += decoder.decode(value, { stream: true });
          const chunks = buffer.split("\n\n");
          buffer = chunks.pop() || "";
          for (const chunk of chunks) {
            const payload = chunk
              .split("\n")
              .filter((line) => line.startsWith("data:"))
              .map((line) => line.slice(5).trimStart())
              .join("\n")
              .trim();
            if (!payload) continue;
            try {
              const parsed = JSON.parse(payload) as { register?: unknown; stats?: unknown };
              const register = parsed.register && typeof parsed.register === "object" ? parsed.register : parsed;
              if (register && typeof register === "object" && "stats" in register) {
                setRegisterConfig(register as Parameters<typeof setRegisterConfig>[0]);
              }
            } catch {
              // Ignore malformed SSE frames and keep the stream alive.
            }
          }
        }
        if (!controller.signal.aborted) {
          throw new Error("register events closed");
        }
      } catch {
        if (!controller.signal.aborted) {
          fallbackTimer = setTimeout(() => {
            void loadRegister(true);
            void readEvents();
          }, 1500);
        }
      }
    };

    void readEvents();
    return () => {
      controller.abort();
      if (fallbackTimer) clearTimeout(fallbackTimer);
    };
  }, [loadRegister, setRegisterConfig]);

  return null;
}

export default function RegisterPage() {
  const { isCheckingAuth, session } = useAuthGuard(["admin"]);

  if (isCheckingAuth || !session || session.role !== "admin") {
    return (
      <div className="flex min-h-[40vh] items-center justify-center">
        <LoaderCircle className="size-5 animate-spin text-stone-400" />
      </div>
    );
  }

  return (
    <main className="mx-auto flex max-w-6xl flex-col gap-5 px-4 py-6 sm:px-6 lg:px-8">
      <RegisterDataController />
      <div className="space-y-1">
        <h1 className="text-2xl font-semibold tracking-tight text-foreground">注册机</h1>
        <p className="text-sm text-muted-foreground">配置自动注册参数，实时查看注册任务进度、账号校验和失败原因。</p>
      </div>
      <RegisterCard />
    </main>
  );
}
