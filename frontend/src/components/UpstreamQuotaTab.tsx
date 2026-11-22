import React, { useState, useEffect, useCallback } from "react";
import {
  Activity,
  AlertCircle,
  CheckCircle2,
  Clock,
  CreditCard,
  DollarSign,
  Gauge,
  Layers,
  RefreshCw,
  Timer,
  Zap,
} from "lucide-react";
import { A6BillingResponse, OpenCodeUsageResponse, OpenCodeUsageWindow } from "../types";

const WINDOWS = [
  { key: "rolling", label: "5 小时窗口", short: "5h" },
  { key: "weekly", label: "周窗口", short: "周" },
  { key: "monthly", label: "月窗口", short: "月" },
];

const clampPercent = (v: number) => Math.max(0, Math.min(100, v));

const formatResetTime = (timeStr?: string) => {
  if (!timeStr) return "重置时间未知";
  try {
    const diffMs = new Date(timeStr).getTime() - Date.now();
    if (diffMs <= 0) return "即将重置";
    const hours = Math.floor(diffMs / (1000 * 60 * 60));
    const mins = Math.floor((diffMs % (1000 * 60 * 60)) / (1000 * 60));
    if (hours > 24) {
      const days = Math.floor(hours / 24);
      return `${days}天 ${hours % 24}小时`;
    }
    return `${hours}小时 ${mins}分`;
  } catch {
    return timeStr;
  }
};

const remainingTone = (remaining: number) => {
  if (remaining > 50) return { bar: "bg-emerald-500", text: "text-emerald-600", bg: "bg-emerald-50", border: "border-emerald-200" };
  if (remaining > 20) return { bar: "bg-amber-500", text: "text-amber-600", bg: "bg-amber-50", border: "border-amber-200" };
  return { bar: "bg-rose-500", text: "text-rose-600", bg: "bg-rose-50", border: "border-rose-200" };
};

const statusBadge = (status?: string) => {
  if (!status || status === "ok") {
    return (
      <span className="inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-[11px] font-medium bg-emerald-50 text-emerald-700 border border-emerald-200">
        <CheckCircle2 className="w-3 h-3" /> 正常
      </span>
    );
  }
  return (
    <span className="inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-[11px] font-medium bg-amber-50 text-amber-700 border border-amber-200">
      <AlertCircle className="w-3 h-3" /> {status}
    </span>
  );
};

export const UpstreamQuotaTab: React.FC = () => {
  const [a6, setA6] = useState<A6BillingResponse | null>(null);
  const [oc, setOc] = useState<OpenCodeUsageResponse | null>(null);
  const [loading, setLoading] = useState<boolean>(false);
  const [error, setError] = useState<string | null>(null);
  const [lastUpdated, setLastUpdated] = useState<string | null>(null);

  const fetchAll = useCallback(async (force = false, silent = false) => {
    if (!silent) setLoading(true);
    setError(null);
    try {
      const suffix = force ? "?refresh=true" : "";
      const [a6Res, ocRes] = await Promise.all([
        fetch(`/api/v1/a6/billing${suffix}`),
        fetch(`/api/v1/opencode/quota${suffix}`),
      ]);

      if (a6Res.ok) {
        setA6(await a6Res.json());
      }
      if (!ocRes.ok) {
        throw new Error(`OpenCode quota HTTP ${ocRes.status}: ${await ocRes.text()}`);
      }
      setOc(await ocRes.json());
      setLastUpdated(new Date().toISOString());
    } catch (err: any) {
      console.error("Failed to fetch upstream quota:", err);
      if (!silent) setError(err?.message || "加载额度失败");
    } finally {
      if (!silent) setLoading(false);
    }
  }, []);

  useEffect(() => {
    fetchAll();
    const id = window.setInterval(() => fetchAll(false, true), 60_000);
    return () => window.clearInterval(id);
  }, [fetchAll]);

  const a6billing = a6?.a6api;

  return (
    <div className="space-y-6 animate-fadeIn">
      {/* Banner */}
      <div className="bg-white rounded-2xl p-6 border border-slate-200/80 shadow-sm flex flex-col md:flex-row md:items-center justify-between gap-4">
        <div className="flex items-start gap-3.5">
          <div className="w-11 h-11 rounded-xl bg-gradient-to-tr from-blue-600 via-indigo-600 to-purple-600 flex items-center justify-center shadow-md shadow-indigo-500/20 text-white">
            <Gauge className="w-6 h-6" />
          </div>
          <div>
            <div className="flex items-center gap-2">
              <h2 className="text-xl font-bold text-slate-900">上游配额与余额</h2>
              <span className="px-2.5 py-0.5 text-xs font-semibold bg-indigo-50 text-indigo-700 border border-indigo-200 rounded-full flex items-center gap-1">
                <Zap className="w-3 h-3 text-indigo-600" /> OpenCode Go + A6
              </span>
            </div>
            <p className="text-sm text-slate-500 mt-1 flex items-center gap-1.5">
              <Activity className="w-3.5 h-3.5 text-slate-400" />
              OpenCode Go 订阅额度（5h / 周 / 月）+ A6 API 账单 · 每 60 秒自动刷新
            </p>
          </div>
        </div>

        <div className="flex items-center gap-3">
          {lastUpdated && (
            <span className="text-xs text-slate-400 flex items-center gap-1">
              <Clock className="w-3.5 h-3.5" />
              上次同步: {new Date(lastUpdated).toLocaleTimeString()}
            </span>
          )}
          <button
            onClick={() => fetchAll(true)}
            disabled={loading}
            className="flex items-center gap-1.5 bg-indigo-600 hover:bg-indigo-700 active:bg-indigo-800 text-white px-4 py-2 rounded-xl text-sm font-semibold transition-all shadow-sm shadow-indigo-600/20 disabled:opacity-50 cursor-pointer"
          >
            <RefreshCw className={`w-4 h-4 ${loading ? "animate-spin" : ""}`} />
            刷新额度
          </button>
        </div>
      </div>

      {/* Error */}
      {error && (
        <div className="bg-rose-50 border border-rose-200 rounded-xl p-4 flex items-center gap-3 text-rose-800 text-sm">
          <AlertCircle className="w-5 h-5 text-rose-600 shrink-0" />
          <span>{error}</span>
        </div>
      )}

      {/* OpenCode Go Quota */}
      <div className="bg-white rounded-2xl border border-slate-200/80 shadow-sm overflow-hidden">
        <div className="px-6 py-4 border-b border-slate-100 flex items-center justify-between gap-3">
          <div className="flex items-center gap-2.5">
            <div className="w-8 h-8 rounded-lg bg-indigo-50 border border-indigo-100 flex items-center justify-center">
              <Gauge className="w-4 h-4 text-indigo-600" />
            </div>
            <div>
              <h3 className="text-base font-bold text-slate-900">OpenCode Go 订阅额度</h3>
              <p className="text-xs text-slate-500">按滚动窗口统计，百分比为已用额度</p>
            </div>
          </div>
          <span className="text-[11px] font-mono text-slate-400">
            {oc?.last_updated ? new Date(oc.last_updated).toLocaleTimeString() : "—"}
          </span>
        </div>

        <div className="p-6 grid grid-cols-1 md:grid-cols-3 gap-4">
          {WINDOWS.map((w) => {
            const win: OpenCodeUsageWindow | undefined = oc?.usage?.[w.key];
            const used = clampPercent(win?.percent ?? 0);
            const remaining = clampPercent(100 - used);
            const tone = remainingTone(remaining);
            return (
              <div key={w.key} className="bg-slate-50/70 border border-slate-200/70 rounded-xl p-4">
                <div className="flex items-center justify-between gap-2 mb-2">
                  <div className="flex items-center gap-1.5">
                    <Timer className="w-4 h-4 text-indigo-500" />
                    <span className="text-sm font-semibold text-slate-800">{w.label}</span>
                    <span className="px-1.5 py-0.5 rounded bg-slate-200/70 text-[10px] font-mono text-slate-600">
                      {w.short}
                    </span>
                  </div>
                  {statusBadge(win?.status)}
                </div>

                <div className="flex items-baseline gap-2">
                  <span className={`text-3xl font-black font-mono ${tone.text}`}>
                    {remaining.toFixed(0)}%
                  </span>
                  <span className="text-xs text-slate-500">剩余</span>
                </div>

                <div className="w-full bg-slate-200/80 rounded-full h-2 overflow-hidden my-3">
                  <div
                    className={`h-full rounded-full transition-all duration-500 ${tone.bar}`}
                    style={{ width: `${remaining}%` }}
                  />
                </div>

                <div className="flex items-center justify-between text-[11px] text-slate-500 font-mono">
                  <span>已用 {used.toFixed(1)}%</span>
                  <span className="flex items-center gap-1">
                    <Clock className="w-3 h-3" />
                    {formatResetTime(win?.resetsAt)}
                  </span>
                </div>
              </div>
            );
          })}
        </div>

        <div className="px-6 pb-5 text-[11px] text-slate-400">
          说明：免费模型（如 <span className="font-mono">hy4-flash-free-oc</span>）不计入额度；额度耗尽时上游返回 429，请关注重置时间。
        </div>
      </div>

      {/* A6 Billing */}
      {a6billing && (
        <div className="bg-gradient-to-r from-slate-900 via-indigo-950 to-slate-900 rounded-2xl p-6 text-white shadow-lg border border-indigo-900/50">
          <div className="flex flex-col md:flex-row md:items-center justify-between pb-5 border-b border-white/10 gap-4">
            <div className="flex items-center gap-3.5">
              <div className="w-10 h-10 rounded-xl bg-indigo-500/20 border border-indigo-400/30 flex items-center justify-center text-indigo-300">
                <CreditCard className="w-5 h-5" />
              </div>
              <div>
                <div className="flex items-center gap-2">
                  <h3 className="text-lg font-bold text-white tracking-wide">A6 API 中转渠道账单与余额</h3>
                  <span
                    className={`px-2 py-0.5 text-xs font-medium rounded-full border flex items-center gap-1 ${
                      a6billing.status === "active"
                        ? "bg-emerald-500/20 border-emerald-400/40 text-emerald-300"
                        : "bg-amber-500/20 border-amber-400/40 text-amber-300"
                    }`}
                  >
                    {a6billing.status === "active" ? <CheckCircle2 className="w-3 h-3" /> : <AlertCircle className="w-3 h-3" />}
                    {a6billing.status}
                  </span>
                </div>
                <p className="text-xs text-indigo-200/70 mt-0.5 font-mono">
                  Endpoint: https://api.a6api.com · 承载 kimi-k3 / glm-5.3 / luna / deepseek
                </p>
              </div>
            </div>
            <div className="text-xs text-indigo-200/60 flex items-center gap-1">
              <Clock className="w-3.5 h-3.5" />
              同步时间: {new Date(a6billing.last_updated).toLocaleTimeString()}
            </div>
          </div>

          <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-4 mt-5">
            <div className="bg-white/5 border border-white/10 rounded-xl p-4 backdrop-blur-sm">
              <div className="text-xs font-medium text-indigo-200/80 flex items-center gap-1 mb-1.5">
                <DollarSign className="w-3.5 h-3.5 text-emerald-400" /> A6 累计总消耗 (USD)
              </div>
              <div className="text-2xl font-black font-mono text-emerald-400">
                ${a6billing.total_usage_usd.toFixed(4)}
              </div>
              <div className="text-[11px] text-white/50 mt-1 font-mono">
                点数: {(a6billing.total_usage_usd * 100).toFixed(2)} pts
              </div>
            </div>

            <div className="bg-white/5 border border-white/10 rounded-xl p-4 backdrop-blur-sm">
              <div className="text-xs font-medium text-indigo-200/80 flex items-center gap-1 mb-1.5">
                <DollarSign className="w-3.5 h-3.5 text-amber-400" /> 折合人民币开销 (CNY)
              </div>
              <div className="text-2xl font-black font-mono text-amber-300">
                ¥{a6billing.total_usage_cny.toFixed(2)}
              </div>
              <div className="text-[11px] text-white/50 mt-1">以当前汇率 7.23 动态对账</div>
            </div>

            <div className="bg-white/5 border border-white/10 rounded-xl p-4 backdrop-blur-sm">
              <div className="text-xs font-medium text-indigo-200/80 flex items-center gap-1 mb-1.5">
                <Layers className="w-3.5 h-3.5 text-blue-400" /> 配额安全阈值 (Hard Limit)
              </div>
              <div className="text-2xl font-black font-mono text-blue-300">
                {a6billing.hard_limit_usd >= 1000000 ? "无限额 / 充沛" : `$${a6billing.hard_limit_usd}`}
              </div>
              <div className="text-[11px] text-white/50 mt-1">无限额软拦截 · 充值扣除</div>
            </div>

            <div className="bg-white/5 border border-white/10 rounded-xl p-4 backdrop-blur-sm">
              <div className="text-xs font-medium text-indigo-200/80 flex items-center gap-1 mb-1.5">
                <CreditCard className="w-3.5 h-3.5 text-purple-400" /> 账户可用性
              </div>
              <div className="text-xl font-bold text-purple-200">
                {a6billing.has_payment_method ? "已绑卡 / 自动扣费" : "余额可用"}
              </div>
              <div className="text-[11px] text-emerald-400/80 mt-1 flex items-center gap-1">
                <CheckCircle2 className="w-3 h-3" /> 授权令牌连接畅通
              </div>
            </div>
          </div>

          {a6billing.error_msg && (
            <div className="mt-4 text-xs text-amber-300/90 bg-amber-500/10 border border-amber-400/30 rounded-lg p-3">
              {a6billing.error_msg}
            </div>
          )}
        </div>
      )}
    </div>
  );
};
