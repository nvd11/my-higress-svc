#!/usr/bin/env python3
"""仓库级明文密钥扫描 (无外部依赖)。

设计原则:
  * 只看 git 已跟踪的文件 (git ls-files), 因此 .gitignore / .git/info/exclude
    里的本地轮换产物不会被误报;
  * 规则足够宽 (sk-/AIza/Bearer/私钥头), 但用一个"占位符白名单"放过
    文档中的示例与测试夹具;
  * 命中即 exit 1, 并把文件名/行号/脱敏片段打到 ::error:: 注解里。

历史教训: 2026-11 本仓库 (PUBLIC) 曾把 16 把 virtual key 明文提交 13 天,
旧的单条正则只扫 wasm-plugin.yaml 且只认一种 key 格式, 完全没兜住。
2026-11-22 补充: 文档中曾出现 sshpass / sudo -S 明文口令, 以及 OpenCode
的 oc_sk_ 新 key 格式; 本脚本已覆盖这些模式。
"""
from __future__ import annotations

import re
import subprocess
import sys

# 允许出现的占位/示例/夹具片段 (大小写不敏感)
ALLOWLIST = (
    "redacted",
    "see-oci-vault",
    "xxx",
    "example",
    "placeholder",
    "your-",
    "test-",
    "forged",
    "dummy",
    "smoke",
    "secret",   # 例如 sk-cindy-secret 单测夹具
    "__env",
    "{{",
    "${",
)

PATTERNS = (
    # OpenAI 风格: sk- 开头 + 长随机串
    re.compile(r"sk-[A-Za-z0-9_-]{20,}"),
    # OpenCode Go / Console key: oc_sk_ 开头
    re.compile(r"oc_sk_[A-Za-z0-9_-]{20,}"),
    # Google API key
    re.compile(r"AIza[0-9A-Za-z_-]{30,}"),
    # 私钥块
    re.compile(r"-----BEGIN [A-Z ]*PRIVATE KEY-----"),
    # sshpass 明文口令
    re.compile(r"sshpass\s+-p\s+['\"]?[^\s'\"]{4,}"),
    # echo <字面量> | sudo -S (明文口令喂给 sudo)
    re.compile(r"echo\s+['\"]?[A-Za-z0-9@#%^&*_.!+-]{6,}['\"]?\s*\|\s*sudo\s+-S"),
    # password / passwd / pwd 直接赋字面量 (>=8 字符; 排除 $VAR / ${...} / <占位>)
    re.compile(r"(?i)\b(?:pass(?:word|wd)|pwd)\s*[:=]\s*['\"]?[A-Za-z0-9!@#$%^&*_+-]{8,}"),
    # getEnv("XXX_PASSWORD", "字面量") 之类的硬编码默认口令
    re.compile(r'(?i)_(?:PASSWORD|PASSWD|PWD)"\s*,\s*"[^"]{6,}"'),
)

# 这些路径本身就是"凭证模板", 命中不算泄露
PATH_ALLOWLIST = (
    "scripts/secret-scan.py",
)


def tracked_files() -> list[str]:
    out = subprocess.run(
        ["git", "ls-files", "-z"], capture_output=True, check=True
    ).stdout
    return [p for p in out.decode().split("\0") if p]


def allowed(line: str, path: str) -> bool:
    if path in PATH_ALLOWLIST or path.startswith(PATH_ALLOWLIST):
        return True
    low = line.lower()
    return any(tok in low for tok in ALLOWLIST)


def main() -> int:
    hits = 0
    for path in tracked_files():
        try:
            with open(path, "r", encoding="utf-8", errors="ignore") as fh:
                for lineno, line in enumerate(fh, 1):
                    for pat in PATTERNS:
                        m = pat.search(line)
                        if not m:
                            continue
                        if allowed(line, path):
                            continue
                        frag = m.group(0)
                        # 只显示前缀, 不回显尾部: CI 日志可能是公开的,
                        # 避免把口令/Key 的后 4 位二次泄露。
                        masked = frag[:8] + "…"
                        print(
                            f"::error file={path},line={lineno}::"
                            f"疑似明文密钥: {masked}"
                        )
                        hits += 1
                        break
        except (OSError, UnicodeError):
            continue

    if hits:
        print(f"\n❌ 共发现 {hits} 处疑似明文密钥, 请迁至 OCI Vault / Secret。")
        return 1
    print("✅ 密钥扫描通过: 仓库内无明文密钥。")
    return 0


if __name__ == "__main__":
    sys.exit(main())
