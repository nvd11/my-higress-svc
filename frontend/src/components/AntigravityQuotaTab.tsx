import React, { useState, useEffect, useCallback } from "react";
import { Sparkles, RefreshCw, AlertCircle, CheckCircle2, Clock, Zap, ShieldAlert, Cpu, Users } from "lucide-react";
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
      setError(err?.message || "Failed to load quota");
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
                Google Antigravity (AGY) 账号配额透视
              </h2>
              <span className="px-2.5 py-0.5 text-xs font-semibold bg-purple-50 text-purple-700 border border-purple-200 rounded-full flex items-center gap-1">
                <Zap className="w-3 h-3 text-purple-600" /> Google AI Pro 订阅
              </span>
            </div>
            <p className="text-sm text-slate-500 mt-1 flex items-center gap-1.5">
              <Users className="w-3.5 h-3.5 text-slate-400" />
              实时同步 {accountCount} 个账号 · Gemini Models 与 Claude &amp; GPT Models 的
              周限额与 5 小时滑动窗口 · 每 60 秒自动刷新
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
