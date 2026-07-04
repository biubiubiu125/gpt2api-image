"use client";

import { LoaderCircle } from "lucide-react";
import { useEffect, useRef } from "react";

import { useAuthGuard } from "@/lib/use-auth-guard";

import { useSettingsStore } from "../settings/store";
import { RegisterCard } from "./components/register-card";

function RegisterDataController() {
  const didLoadRef = useRef(false);
  const loadRegister = useSettingsStore((state) => state.loadRegister);

  useEffect(() => {
    if (didLoadRef.current) return;
    didLoadRef.current = true;
    void loadRegister();
  }, [loadRegister]);

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
        <p className="text-sm text-muted-foreground">配置自动注册参数；Go 版执行器接入前，启动会明确返回未迁移状态。</p>
      </div>
      <RegisterCard />
    </main>
  );
}
