"use client";

import { useEffect, useMemo, useState } from "react";
import {
  CheckCircle2,
  Copy,
  Eye,
  KeyRound,
  LoaderCircle,
  Pencil,
  Plus,
  RefreshCw,
  Search,
  ShieldCheck,
  Trash2,
} from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import {
  createUserKey,
  deleteUserKey,
  fetchUserKeyPlaintext,
  fetchUserKeys,
  regenerateUserKey,
  updateUserKey,
  type UserKey,
} from "@/lib/api";
import { cn } from "@/lib/utils";

let cachedItems: UserKey[] | null = null;

const PAGE_SIZE = 8;

type KeyDialogState = {
  item: UserKey;
  value: string;
};

export function UserKeysCard() {
  const [items, setItems] = useState<UserKey[]>(cachedItems ?? []);
  const [isLoading, setIsLoading] = useState(!cachedItems);
  const [query, setQuery] = useState("");
  const [page, setPage] = useState(1);
  const [createOpen, setCreateOpen] = useState(false);
  const [createName, setCreateName] = useState("");
  const [createKey, setCreateKey] = useState("");
  const [editItem, setEditItem] = useState<UserKey | null>(null);
  const [editName, setEditName] = useState("");
  const [resetItem, setResetItem] = useState<UserKey | null>(null);
  const [resetKey, setResetKey] = useState("");
  const [deleteItem, setDeleteItem] = useState<UserKey | null>(null);
  const [plainDialog, setPlainDialog] = useState<KeyDialogState | null>(null);
  const [busyId, setBusyId] = useState<string | null>(null);
  const [isSubmitting, setIsSubmitting] = useState(false);

  useEffect(() => {
    void loadItems(!cachedItems);
  }, []);

  useEffect(() => {
    setPage(1);
  }, [query]);

  const filteredItems = useMemo(() => {
    const keyword = query.trim().toLowerCase();
    if (!keyword) return items;
    return items.filter((item) =>
      [item.name, item.id, item.enabled ? "启用" : "停用"]
        .join(" ")
        .toLowerCase()
        .includes(keyword),
    );
  }, [items, query]);

  const pageCount = Math.max(1, Math.ceil(filteredItems.length / PAGE_SIZE));
  const currentPage = Math.min(page, pageCount);
  const pageItems = filteredItems.slice((currentPage - 1) * PAGE_SIZE, currentPage * PAGE_SIZE);

  async function loadItems(showLoading = true) {
    if (showLoading) setIsLoading(true);
    try {
      const data = await fetchUserKeys();
      cachedItems = data.items;
      setItems(data.items);
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "加载 API 密钥失败");
    } finally {
      if (showLoading) setIsLoading(false);
    }
  }

  async function copyText(value: string, label = "已复制") {
    try {
      await navigator.clipboard.writeText(value);
      toast.success(label);
    } catch {
      toast.error("复制失败，请手动复制");
    }
  }

  async function handleCreate() {
    setIsSubmitting(true);
    try {
      const data = await createUserKey({
        name: createName.trim() || "API 密钥",
        key: createKey.trim() || undefined,
      });
      cachedItems = data.items;
      setItems(data.items);
      setCreateOpen(false);
      setCreateName("");
      setCreateKey("");
      setPlainDialog({ item: data.item, value: data.key });
      toast.success("API 密钥已创建");
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "创建 API 密钥失败");
    } finally {
      setIsSubmitting(false);
    }
  }

  async function handleToggle(item: UserKey) {
    setBusyId(item.id);
    try {
      const data = await updateUserKey(item.id, { enabled: !item.enabled });
      cachedItems = data.items;
      setItems(data.items);
      toast.success(item.enabled ? "API 密钥已停用" : "API 密钥已启用");
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "更新 API 密钥失败");
    } finally {
      setBusyId(null);
    }
  }

  function openEdit(item: UserKey) {
    setEditItem(item);
    setEditName(item.name);
  }

  async function handleEdit() {
    if (!editItem) return;
    const name = editName.trim();
    if (!name) {
      toast.error("名称不能为空");
      return;
    }
    setIsSubmitting(true);
    try {
      const data = await updateUserKey(editItem.id, { name });
      cachedItems = data.items;
      setItems(data.items);
      setEditItem(null);
      toast.success("API 密钥已更新");
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "更新 API 密钥失败");
    } finally {
      setIsSubmitting(false);
    }
  }

  async function handleShowKey(item: UserKey) {
    setBusyId(item.id);
    try {
      const data = await fetchUserKeyPlaintext(item.id);
      if (!data.key_visible || !data.key) {
        toast.error("这条旧密钥没有保存原文，请重置后再复制");
        return;
      }
      setPlainDialog({ item, value: data.key });
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "读取 API 密钥失败");
    } finally {
      setBusyId(null);
    }
  }

  async function handleReset() {
    if (!resetItem) return;
    setIsSubmitting(true);
    try {
      const data = await regenerateUserKey(resetItem.id, resetKey.trim() || undefined);
      cachedItems = data.items;
      setItems(data.items);
      setPlainDialog({ item: data.item, value: data.key });
      setResetItem(null);
      setResetKey("");
      toast.success("API 密钥已重置");
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "重置 API 密钥失败");
    } finally {
      setIsSubmitting(false);
    }
  }

  async function handleDelete() {
    if (!deleteItem) return;
    setIsSubmitting(true);
    try {
      const data = await deleteUserKey(deleteItem.id);
      cachedItems = data.items;
      setItems(data.items);
      setDeleteItem(null);
      toast.success("API 密钥已删除");
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "删除 API 密钥失败");
    } finally {
      setIsSubmitting(false);
    }
  }

  return (
    <>
      <Card className="overflow-hidden rounded-2xl border-border/80 shadow-[0_18px_50px_-28px_rgba(15,23,42,0.25)]">
        <CardContent className="p-0">
          <div className="flex flex-col gap-4 border-b border-border/70 p-4 md:flex-row md:items-center md:justify-between">
            <div className="min-w-0 space-y-1">
              <div className="flex items-center gap-2 text-sm font-medium text-foreground">
                <KeyRound className="size-4 text-muted-foreground" />
                后端调用密钥
              </div>
              <p className="text-xs text-muted-foreground">
                所有密钥都是服务密钥，可用于 newapi 或其他下游渠道调用和管理本后端。
              </p>
            </div>
            <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
              <div className="relative">
                <Search className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-muted-foreground" />
                <Input
                  value={query}
                  onChange={(event) => setQuery(event.target.value)}
                  placeholder="搜索名称或 ID"
                  className="h-9 w-full rounded-lg pl-9 sm:w-56"
                />
              </div>
              <Button onClick={() => setCreateOpen(true)} className="h-9 rounded-lg">
                <Plus className="size-4" />
                新建密钥
              </Button>
            </div>
          </div>

          {isLoading ? (
            <div className="flex h-56 items-center justify-center">
              <LoaderCircle className="size-5 animate-spin text-muted-foreground" />
            </div>
          ) : pageItems.length === 0 ? (
            <div className="flex h-56 flex-col items-center justify-center gap-2 px-4 text-center">
              <KeyRound className="size-6 text-muted-foreground" />
              <div className="text-sm font-medium text-foreground">暂无 API 密钥</div>
              <div className="text-xs text-muted-foreground">创建一个密钥后，下游请求必须通过 Bearer 方式携带它。</div>
            </div>
          ) : (
            <div className="divide-y divide-border/70">
              {pageItems.map((item) => (
                <div key={item.id} className="grid gap-3 p-4 lg:grid-cols-[minmax(0,1fr)_auto] lg:items-center">
                  <div className="min-w-0 space-y-2">
                    <div className="flex flex-wrap items-center gap-2">
                      <div className="truncate text-sm font-semibold text-foreground">{item.name || "API 密钥"}</div>
                      <Badge variant={item.enabled ? "success" : "secondary"}>{item.enabled ? "启用" : "停用"}</Badge>
                      <Badge variant="info" className="gap-1">
                        <ShieldCheck className="size-3" />
                        管理员权限
                      </Badge>
                      <Badge variant="outline">不限额</Badge>
                    </div>
                    <div className="flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted-foreground">
                      <span className="font-data">ID: {item.id}</span>
                      <span>创建: {formatDate(item.created_at)}</span>
                      <span>最后使用: {formatDate(item.last_used_at)}</span>
                      <span>{item.key_visible ? "可查看原文" : "仅保存哈希"}</span>
                    </div>
                  </div>

                  <div className="flex flex-wrap items-center gap-2 lg:justify-end">
                    <Button
                      type="button"
                      variant="outline"
                      size="sm"
                      className="rounded-lg"
                      onClick={() => void handleShowKey(item)}
                      disabled={busyId === item.id}
                    >
                      <Eye className="size-4" />
                      查看
                    </Button>
                    <Button
                      type="button"
                      variant="outline"
                      size="sm"
                      className="rounded-lg"
                      onClick={() => openEdit(item)}
                    >
                      <Pencil className="size-4" />
                      编辑
                    </Button>
                    <Button
                      type="button"
                      variant="outline"
                      size="sm"
                      className="rounded-lg"
                      onClick={() => setResetItem(item)}
                    >
                      <RefreshCw className="size-4" />
                      重置
                    </Button>
                    <Button
                      type="button"
                      variant={item.enabled ? "secondary" : "outline"}
                      size="sm"
                      className="rounded-lg"
                      onClick={() => void handleToggle(item)}
                      disabled={busyId === item.id}
                    >
                      <CheckCircle2 className={cn("size-4", item.enabled ? "text-emerald-600" : "text-muted-foreground")} />
                      {item.enabled ? "停用" : "启用"}
                    </Button>
                    <Button
                      type="button"
                      variant="destructive"
                      size="sm"
                      className="rounded-lg"
                      onClick={() => setDeleteItem(item)}
                    >
                      <Trash2 className="size-4" />
                      删除
                    </Button>
                  </div>
                </div>
              ))}
            </div>
          )}

          <div className="flex flex-col gap-2 border-t border-border/70 px-4 py-3 text-xs text-muted-foreground sm:flex-row sm:items-center sm:justify-between">
            <span>
              共 {filteredItems.length} 条，当前第 {currentPage} / {pageCount} 页
            </span>
            <div className="flex items-center gap-2">
              <Button
                type="button"
                variant="outline"
                size="sm"
                className="h-8 rounded-lg"
                onClick={() => setPage((value) => Math.max(1, value - 1))}
                disabled={currentPage <= 1}
              >
                上一页
              </Button>
              <Button
                type="button"
                variant="outline"
                size="sm"
                className="h-8 rounded-lg"
                onClick={() => setPage((value) => Math.min(pageCount, value + 1))}
                disabled={currentPage >= pageCount}
              >
                下一页
              </Button>
            </div>
          </div>
        </CardContent>
      </Card>

      <Dialog open={createOpen} onOpenChange={setCreateOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>新建 API 密钥</DialogTitle>
            <DialogDescription>留空密钥内容时，系统会自动生成一个 sk- 开头的密钥。</DialogDescription>
          </DialogHeader>
          <div className="space-y-3">
            <Input value={createName} onChange={(event) => setCreateName(event.target.value)} placeholder="名称，例如：newapi 生图通道" />
            <Input value={createKey} onChange={(event) => setCreateKey(event.target.value)} placeholder="自定义密钥，可留空自动生成" />
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => setCreateOpen(false)} disabled={isSubmitting}>
              取消
            </Button>
            <Button type="button" onClick={() => void handleCreate()} disabled={isSubmitting}>
              {isSubmitting ? <LoaderCircle className="size-4 animate-spin" /> : <Plus className="size-4" />}
              创建
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={Boolean(editItem)} onOpenChange={(open) => !open && setEditItem(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>编辑 API 密钥</DialogTitle>
            <DialogDescription>这里只修改显示名称，不改变密钥内容。</DialogDescription>
          </DialogHeader>
          <Input value={editName} onChange={(event) => setEditName(event.target.value)} placeholder="密钥名称" />
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => setEditItem(null)} disabled={isSubmitting}>
              取消
            </Button>
            <Button type="button" onClick={() => void handleEdit()} disabled={isSubmitting}>
              {isSubmitting ? <LoaderCircle className="size-4 animate-spin" /> : <Pencil className="size-4" />}
              保存
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={Boolean(resetItem)} onOpenChange={(open) => !open && setResetItem(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>重置 API 密钥</DialogTitle>
            <DialogDescription>重置后旧密钥会立即失效。留空则自动生成新密钥。</DialogDescription>
          </DialogHeader>
          <Input value={resetKey} onChange={(event) => setResetKey(event.target.value)} placeholder="自定义新密钥，可留空自动生成" />
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => setResetItem(null)} disabled={isSubmitting}>
              取消
            </Button>
            <Button type="button" onClick={() => void handleReset()} disabled={isSubmitting}>
              {isSubmitting ? <LoaderCircle className="size-4 animate-spin" /> : <RefreshCw className="size-4" />}
              重置
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={Boolean(deleteItem)} onOpenChange={(open) => !open && setDeleteItem(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>删除 API 密钥</DialogTitle>
            <DialogDescription>删除后使用这条密钥的下游渠道会立刻无法调用。</DialogDescription>
          </DialogHeader>
          <div className="rounded-lg border border-border bg-muted/40 px-3 py-2 text-sm text-foreground">
            {deleteItem?.name || "API 密钥"}
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => setDeleteItem(null)} disabled={isSubmitting}>
              取消
            </Button>
            <Button type="button" variant="destructive" onClick={() => void handleDelete()} disabled={isSubmitting}>
              {isSubmitting ? <LoaderCircle className="size-4 animate-spin" /> : <Trash2 className="size-4" />}
              删除
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={Boolean(plainDialog)} onOpenChange={(open) => !open && setPlainDialog(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>API 密钥</DialogTitle>
            <DialogDescription>密钥只应配置给可信的 newapi 或下游服务。</DialogDescription>
          </DialogHeader>
          <div className="rounded-lg border border-border bg-muted/40 p-3 font-data text-xs break-all text-foreground">
            {plainDialog?.value}
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => setPlainDialog(null)}>
              关闭
            </Button>
            <Button type="button" onClick={() => plainDialog && void copyText(plainDialog.value)}>
              <Copy className="size-4" />
              复制
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}

function formatDate(value?: string | null) {
  if (!value) return "-";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "-";
  return date.toLocaleString("zh-CN", {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
  });
}
