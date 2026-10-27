import React, { useState, useEffect, useCallback } from "react";
import { Sparkles, RefreshCw, AlertCircle, CheckCircle2, Clock, Zap, ShieldAlert, Cpu, Users, CreditCard, DollarSign, Layers } from "lucide-react";
import { AntigravityQuotaResponse, AntigravityBucket } from "../types";

/** Server sends English display names; map the known bucket ids to zh-CN. */
const BUCKET_LABELS: Record<string, string> = {
  "gemini-weekly": "周限额剩余",
  "gemini-5h": "5 小时限额剩余",
  "3p-weekly": "周限额剩余",
  "3p-5h": "5 小时限额剩余",
};

const bucketLabel = (b: AntigravityBucket) =>
  BUCKET_LABELS[b.bucket_id] ?? b.display_name ?? b.bucket_id;

const windowBadge = (w: string) => (w === "weekly" ? "周" : w === "5h" ? "5h" : w);

export const AntigravityQuotaTab: React.FC = () => {
  const [data, setData] = useState<AntigravityQuotaResponse | null>(null);
  const [loading, setLoading] = useState<boolean>(false);
  const [error, setError] = useState<string | null>(null);

  const fetchQuota = useCallback(async (force = false, silent = false) => {
    try {
      if (!silent) setLoading(true);
      setError(null);
      const res = await fetch(`/api/v1/antigravity/quota${force ? "?refresh=true" : ""}`);
      if (!res.ok) {
        throw new Error(`HTTP ${res.status}: ${await res.text()}`);
      }
      const json: AntigravityQuotaResponse = await res.json();
      setData(json);
    } catch (err: any) {
      console.error("Failed to fetch antigravity quota:", err);
      // 如果已经有历史数据，遇到偶发网络波动则静默忽略报错，避免刺眼红框打扰
      setData((prev) => {
        if (!prev) {
          setError(err?.message || "Failed to load quota");
        }
        return prev;
      });
    } finally {
      if (!silent) setLoading(false);
    }
  }, []);

  // Initial load + silent poll every 60s (backend caches for 60s).
  useEffect(() => {
    fetchQuota();
    const id = window.setInterval(() => fetchQuota(false, true), 60_000);
    return () => window.clearInterval(id);
  }, [fetchQuota]);

  const formatPercentage = (fraction: number) => {
    return (Math.max(0, Math.min(1, fraction)) * 100).toFixed(2);
  };

  const getProgressColor = (fraction: number) => {
    if (fraction > 0.6) return "bg-emerald-500";
    if (fraction > 0.25) return "bg-amber-500";
    return "bg-rose-500";
  };

  const getTextColor = (fraction: number) => {
    if (fraction > 0.6) return "text-emerald-700";
    if (fraction > 0.25) return "text-amber-700";
    return "text-rose-700";
  };

  const formatResetTime = (timeStr: string) => {
    if (!timeStr) return null;
    try {
      const resetDate = new Date(timeStr);
      const diffMs = resetDate.getTime() - Date.now();
      if (diffMs <= 0) return "即将重置 / 额度已恢复";

      const hours = Math.floor(diffMs / (1000 * 60 * 60));
      const mins = Math.floor((diffMs % (1000 * 60 * 60)) / (1000 * 60));

      if (hours > 24) {
        const days = Math.floor(hours / 24);
        const remHours = hours % 24;
        return `刷新倒计时: ${days}天 ${remHours}小时`;
      }
      return `刷新倒计时: ${hours}小时 ${mins}分`;
    } catch {
      return timeStr;
    }
  };

  const accountCount = data?.accounts?.length ?? 0;

  return (
    <div className="space-y-6 animate-fadeIn">
      {/* Top Banner */}
      <div className="bg-white rounded-2xl p-6 border border-slate-200/80 shadow-sm flex flex-col md:flex-row md:items-center justify-between gap-4">
        <div className="flex items-start gap-3.5">
          <div className="w-11 h-11 rounded-xl bg-gradient-to-tr from-purple-600 via-indigo-600 to-blue-500 flex items-center justify-center shadow-md shadow-indigo-500/20 text-white">
            <Sparkles className="w-6 h-6 animate-pulse" />
          </div>
          <div>
            <div className="flex items-center gap-2">
              <h2 className="text-xl font-bold text-slate-900">
                上游渠道与 Google Antigravity 配额透视
              </h2>
              <span className="px-2.5 py-0.5 text-xs font-semibold bg-purple-50 text-purple-700 border border-purple-200 rounded-full flex items-center gap-1">
                <Zap className="w-3 h-3 text-purple-600" /> 配额与财务双核监控
              </span>
            </div>
            <p className="text-sm text-slate-500 mt-1 flex items-center gap-1.5">
              <Users className="w-3.5 h-3.5 text-slate-400" />
              聚合 A6 API 中转渠道财务账单 &amp; {accountCount} 个 Antigravity 账号滑动窗口配额 · 每 60 秒自动刷新
            </p>
          </div>
        </div>

        <div className="flex items-center gap-3">
          {data?.last_updated && (
            <span className="text-xs text-slate-400 flex items-center gap-1">
              <Clock className="w-3.5 h-3.5" />
              上次同步: {new Date(data.last_updated).toLocaleTimeString()}
            </span>
          )}
          <button
            onClick={() => fetchQuota(true)}
            disabled={loading}
            className="flex items-center gap-1.5 bg-indigo-600 hover:bg-indigo-700 active:bg-indigo-800 text-white px-4 py-2 rounded-xl text-sm font-semibold transition-all shadow-sm shadow-indigo-600/20 disabled:opacity-50 cursor-pointer"
          >
            <RefreshCw className={`w-4 h-4 ${loading ? "animate-spin" : ""}`} />
            强制刷新配额
          </button>
        </div>
      </div>

      {/* A6 API Billing & Channel Card */}
      {data?.a6api && (
        <div className="bg-gradient-to-r from-slate-900 via-indigo-950 to-slate-900 rounded-2xl p-6 text-white shadow-lg border border-indigo-900/50">
          <div className="flex flex-col md:flex-row md:items-center justify-between pb-5 border-b border-white/10 gap-4">
            <div className="flex items-center gap-3.5">
              <div className="w-10 h-10 rounded-xl bg-indigo-500/20 border border-indigo-400/30 flex items-center justify-center text-indigo-300">
                <CreditCard className="w-5 h-5" />
              </div>
              <div>
                <div className="flex items-center gap-2">
                  <h3 className="text-lg font-bold text-white tracking-wide">
                    A6 API 中转渠道账单与余额
                  </h3>
                  <span className="px-2 py-0.5 text-xs font-medium rounded-full bg-emerald-500/20 border border-emerald-400/40 text-emerald-300 flex items-center gap-1">
                    <CheckCircle2 className="w-3 h-3 text-emerald-400" />
                    正常活跃
                  </span>
                </div>
                <p className="text-xs text-indigo-200/70 mt-0.5 font-mono">
                  Endpoint: https://api.a6api.com · 承载 kimi-k3 / glm-5.3 / luna / deepseek
                </p>
              </div>
            </div>

            <div className="text-xs text-indigo-200/60 flex items-center gap-1 self-start md:self-auto">
              <Clock className="w-3.5 h-3.5" />
              同步时间: {new Date(data.a6api.last_updated).toLocaleTimeString()}
            </div>
          </div>

          <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-4 mt-5">
            {/* 累计总消耗 USD */}
            <div className="bg-white/5 border border-white/10 rounded-xl p-4 backdrop-blur-sm">
              <div className="text-xs font-medium text-indigo-200/80 flex items-center gap-1 mb-1.5">
                <DollarSign className="w-3.5 h-3.5 text-emerald-400" />
                A6 累计总消耗 (USD)
              </div>
              <div className="text-2xl font-black font-mono text-emerald-400">
                ${data.a6api.total_usage_usd.toFixed(4)}
              </div>
              <div className="text-[11px] text-white/50 mt-1 font-mono">
                点数: {(data.a6api.total_usage_usd * 100).toFixed(2)} pts
              </div>
            </div>

            {/* 折合人民币 CNY */}
            <div className="bg-white/5 border border-white/10 rounded-xl p-4 backdrop-blur-sm">
              <div className="text-xs font-medium text-indigo-200/80 flex items-center gap-1 mb-1.5">
                <DollarSign className="w-3.5 h-3.5 text-amber-400" />
                折合人民币开销 (CNY)
              </div>
              <div className="text-2xl font-black font-mono text-amber-300">
                ¥{data.a6api.total_usage_cny.toFixed(2)}
              </div>
              <div className="text-[11px] text-white/50 mt-1">
                以当前汇率 7.23 动态对账
              </div>
            </div>

            {/* 限额政策与状态 */}
            <div className="bg-white/5 border border-white/10 rounded-xl p-4 backdrop-blur-sm">
              <div className="text-xs font-medium text-indigo-200/80 flex items-center gap-1 mb-1.5">
                <Layers className="w-3.5 h-3.5 text-blue-400" />
                配额安全阈值 (Hard Limit)
              </div>
              <div className="text-2xl font-black font-mono text-blue-300">
                {data.a6api.hard_limit_usd >= 1000000 ? "无限额 / 充沛" : `$${data.a6api.hard_limit_usd}`}
              </div>
              <div className="text-[11px] text-white/50 mt-1">
                无限额软拦截 · 充值扣除
              </div>
            </div>

            {/* 支付状态 */}
            <div className="bg-white/5 border border-white/10 rounded-xl p-4 backdrop-blur-sm">
              <div className="text-xs font-medium text-indigo-200/80 flex items-center gap-1 mb-1.5">
                <CreditCard className="w-3.5 h-3.5 text-purple-400" />
                账户可用性
              </div>
              <div className="text-xl font-bold text-purple-200">
                {data.a6api.has_payment_method ? "已绑卡 / 自动扣费" : "余额可用"}
              </div>
              <div className="text-[11px] text-emerald-400/80 mt-1 flex items-center gap-1">
                <CheckCircle2 className="w-3 h-3" /> 授权令牌连接畅通
              </div>
            </div>
          </div>
        </div>
      )}

      {/* Error Notice */}
      {error && (
        <div className="bg-rose-50 border border-rose-200 rounded-xl p-4 flex items-center gap-3 text-rose-800 text-sm">
          <AlertCircle className="w-5 h-5 text-rose-600 shrink-0" />
          <span>获取配额失败: {error}</span>
        </div>
      )}

      {/* Loading Skeleton */}
      {loading && !data && (
        <div className="grid grid-cols-1 lg:grid-cols-2 gap-6">
          {[1, 2].map((idx) => (
            <div key={idx} className="bg-white rounded-2xl p-6 border border-slate-200/80 shadow-sm space-y-4 animate-pulse">
              <div className="h-6 bg-slate-100 rounded w-1/3"></div>
              <div className="h-24 bg-slate-50 rounded-xl"></div>
              <div className="h-24 bg-slate-50 rounded-xl"></div>
            </div>
          ))}
        </div>
      )}

      {/* Accounts List */}
      <div className="grid grid-cols-1 lg:grid-cols-2 gap-6">
        {data?.accounts?.map((acc) => {
          const isHealthy = acc.status === "healthy";
          return (
            <div
              key={acc.email}
              className="bg-white rounded-2xl border border-slate-200/90 shadow-sm overflow-hidden flex flex-col transition-all hover:shadow-md hover:border-indigo-200"
            >
              {/* Account Header */}
              <div className="p-5 border-b border-slate-100 bg-slate-50/50 flex items-center justify-between">
                <div className="flex items-center gap-3">
                  <div className="w-10 h-10 rounded-xl bg-white border border-slate-200 flex items-center justify-center font-bold text-slate-700 shadow-sm">
                    {acc.email.slice(0, 2).toUpperCase()}
                  </div>
                  <div>
                    <div className="flex items-center gap-2">
                      <span className="font-semibold text-slate-900 text-base">
                        {acc.email}
                      </span>
                      {isHealthy ? (
                        <span className="inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-xs font-medium bg-emerald-50 text-emerald-700 border border-emerald-200">
                          <CheckCircle2 className="w-3 h-3 text-emerald-600" />
                          健康活跃
                        </span>
                      ) : (
                        <span className="inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-xs font-medium bg-amber-50 text-amber-700 border border-amber-200">
                          <ShieldAlert className="w-3 h-3 text-amber-600" />
                          {acc.status}
                        </span>
                      )}
                    </div>
                    <p className="text-xs text-slate-400 mt-0.5">
                      物理位置: OCI Vault 凭据托管 · K3s Round-Robin 节点
                    </p>
                  </div>
                </div>
              </div>

              {/* Error in Account */}
              {acc.error_msg && (
                <div className="m-5 p-3 bg-rose-50 border border-rose-100 rounded-xl text-xs text-rose-700">
                  {acc.error_msg}
                </div>
              )}

              {/* Model Groups */}
              <div className="p-5 space-y-6 flex-1">
                {acc.groups?.length === 0 && !acc.error_msg && (
                  <div className="text-center py-8 text-slate-400 text-sm">
                    暂未同步到分组配额，请点击右上角刷新重试
                  </div>
                )}

                {acc.groups?.map((group) => (
                  <div key={group.display_name} className="space-y-3.5">
                    <div className="flex items-center justify-between border-b border-slate-100 pb-2 gap-3">
                      <div className="flex items-center gap-2 shrink-0">
                        <Cpu className="w-4 h-4 text-indigo-600" />
                        <h4 className="text-sm font-bold text-slate-800">
                          {group.display_name}
                        </h4>
                      </div>
                      <span className="text-xs text-slate-400 line-clamp-1 max-w-[240px]" title={group.description}>
                        {group.description}
                      </span>
                    </div>

                    <div className="grid grid-cols-1 sm:grid-cols-2 gap-3.5">
                      {group.buckets?.map((bucket) => {
                        const pctStr = formatPercentage(bucket.remaining_fraction);
                        const frac = bucket.remaining_fraction;
                        const usedStr = formatPercentage(1 - frac);
                        const resetMsg = formatResetTime(bucket.reset_time);

                        return (
                          <div
                            key={bucket.bucket_id}
                            className="bg-slate-50/80 hover:bg-slate-100/70 transition-colors border border-slate-200/60 rounded-xl p-3.5 flex flex-col justify-between"
                          >
                            <div>
                              <div className="flex items-center justify-between text-xs mb-2 gap-2">
                                <span className="font-semibold text-slate-700 flex items-center gap-1.5">
                                  {bucketLabel(bucket)}
                                  <span className="px-1.5 py-0.5 rounded bg-slate-200/70 text-[10px] font-mono text-slate-600">
                                    {windowBadge(bucket.window)}
                                  </span>
                                </span>
                                <span className={`font-mono font-bold text-sm shrink-0 ${getTextColor(frac)}`}>
                                  {pctStr}%
                                </span>
                              </div>

                              {/* Progress Bar */}
                              <div className="w-full bg-slate-200/80 rounded-full h-2 overflow-hidden mb-2">
                                <div
                                  className={`h-full rounded-full transition-all duration-500 ${getProgressColor(frac)}`}
                                  style={{ width: `${pctStr}%` }}
                                ></div>
                              </div>

                              <div className="text-[11px] text-slate-400 font-mono">
                                已用 {usedStr}% · 剩余 {pctStr}%
                              </div>
                            </div>

                            {/* Reset info */}
                            {resetMsg && (
                              <div className="text-[11px] text-slate-500 font-medium flex items-center gap-1 mt-1 pt-1 border-t border-slate-200/40">
                                <Clock className="w-3 h-3 text-slate-400 shrink-0" />
                                <span className="truncate">{resetMsg}</span>
                              </div>
                            )}
                          </div>
                        );
                      })}
                    </div>
                  </div>
                ))}
              </div>
            </div>
          );
        })}
      </div>
    </div>
  );
};
