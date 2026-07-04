"use client";

import { useEffect, useState } from "react";
import { LoaderCircle, RefreshCw } from "lucide-react";

import { Button } from "@/components/ui/button";
import { useAuthGuard } from "@/lib/use-auth-guard";
import { fetchImageTasks, type ImageTask } from "@/lib/api";

export default function TasksPage() {
  const { isCheckingAuth, session } = useAuthGuard(["admin", "user"]);
  const [items, setItems] = useState<ImageTask[]>([]);
  const [isLoading, setIsLoading] = useState(false);
  const [error, setError] = useState("");

  const load = async () => {
    setIsLoading(true);
    setError("");
    try {
      const data = await fetchImageTasks([]);
      setItems(data.items || []);
    } catch (err) {
      setError(err instanceof Error ? err.message : "加载任务失败");
    } finally {
      setIsLoading(false);
    }
  };

  useEffect(() => {
    if (!session) return;
    void load();
    const timer = window.setInterval(() => void load(), 3000);
    return () => window.clearInterval(timer);
  }, [session]);

  if (isCheckingAuth || !session) {
    return (
      <main className="flex min-h-screen items-center justify-center pt-16">
        <LoaderCircle className="size-5 animate-spin text-muted-foreground" />
      </main>
    );
  }

  return (
    <main className="min-h-screen px-4 pt-20 pb-10 sm:px-6 lg:px-8">
      <div className="mx-auto max-w-6xl space-y-4">
        <div className="flex items-center justify-between gap-3">
          <div>
            <h1 className="text-xl font-semibold tracking-tight">图片任务</h1>
            <p className="mt-1 text-sm text-muted-foreground">查看异步生图队列和最近任务结果。</p>
          </div>
          <Button type="button" variant="outline" onClick={() => void load()} disabled={isLoading}>
            {isLoading ? <LoaderCircle className="size-4 animate-spin" /> : <RefreshCw className="size-4" />}
            刷新
          </Button>
        </div>
        {error ? <div className="rounded-md border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-700">{error}</div> : null}
        <div className="overflow-hidden rounded-lg border border-border bg-card">
          <table className="w-full text-left text-sm">
            <thead className="border-b border-border bg-muted/40 text-xs text-muted-foreground">
              <tr>
                <th className="px-3 py-2">任务 ID</th>
                <th className="px-3 py-2">状态</th>
                <th className="px-3 py-2">模式</th>
                <th className="px-3 py-2">模型</th>
                <th className="px-3 py-2">尺寸</th>
                <th className="px-3 py-2">更新时间</th>
                <th className="px-3 py-2">错误</th>
              </tr>
            </thead>
            <tbody>
              {items.length === 0 ? (
                <tr>
                  <td colSpan={7} className="px-3 py-10 text-center text-muted-foreground">暂无任务</td>
                </tr>
              ) : (
                items.map((item) => (
                  <tr key={item.id} className="border-b border-border/70 last:border-0">
                    <td className="max-w-[220px] truncate px-3 py-2 font-mono text-xs">{item.id}</td>
                    <td className="px-3 py-2">{item.status}</td>
                    <td className="px-3 py-2">{item.mode}</td>
                    <td className="px-3 py-2">{item.model || "-"}</td>
                    <td className="px-3 py-2">{item.size || item.resolution || "-"}</td>
                    <td className="px-3 py-2">{item.updated_at || "-"}</td>
                    <td className="max-w-[260px] truncate px-3 py-2 text-red-600">{item.error || "-"}</td>
                  </tr>
                ))
              )}
            </tbody>
          </table>
        </div>
      </div>
    </main>
  );
}
