"""查看/切换 OKX Demo 账户模式（acctLv：1=现货 2=现货和合约 3=跨币种 4=组合保证金）。

    python scripts/okx_account_mode.py           # 查看当前模式
    python scripts/okx_account_mode.py --set 2   # 切换到 现货和合约模式
"""

from __future__ import annotations

import argparse
import asyncio
import contextlib
import sys

from cyp.config import Settings

LEVELS = {"1": "现货模式", "2": "现货和合约模式", "3": "跨币种保证金模式", "4": "组合保证金模式"}


async def main_async(set_level: str | None) -> int:
    import ccxt.async_support as ccxt
    s = Settings()
    import os
    proxy = os.environ.get("HTTPS_PROXY") or os.environ.get("HTTP_PROXY")
    cfg = {"apiKey": s.okx_api_key, "secret": s.okx_api_secret,
           "password": s.okx_password, "enableRateLimit": True}
    if proxy:
        cfg["httpsProxy"] = proxy
    ex = ccxt.okx(cfg)
    ex.set_sandbox_mode(True)
    try:
        conf = (await ex.privateGetAccountConfig())["data"][0]
        lv = conf.get("acctLv")
        print(f"当前账户模式 acctLv={lv}（{LEVELS.get(lv, '未知')}）  posMode={conf.get('posMode')}")
        if set_level and set_level != lv:
            res = await ex.privatePostAccountSetAccountLevel({"acctLv": set_level})
            print(f"切换结果：{res.get('code')} {res.get('msg') or 'ok'}")
            conf = (await ex.privateGetAccountConfig())["data"][0]
            lv2 = conf.get("acctLv")
            print(f"切换后 acctLv={lv2}（{LEVELS.get(lv2, '未知')}）")
            if conf.get("posMode") != "net_mode":
                res2 = await ex.privatePostAccountSetPositionMode({"posMode": "net_mode"})
                print(f"持仓模式 → net_mode：{res2.get('code')} {res2.get('msg') or 'ok'}")
            return 0 if lv2 == set_level else 1
        return 0
    finally:
        await ex.close()


def main() -> int:
    for stream in (sys.stdout, sys.stderr):
        with contextlib.suppress(Exception):
            stream.reconfigure(encoding="utf-8")  # type: ignore[attr-defined]
    parser = argparse.ArgumentParser()
    parser.add_argument("--set", dest="set_level", default=None, choices=["1", "2", "3", "4"])
    args = parser.parse_args()
    return asyncio.run(main_async(args.set_level))


if __name__ == "__main__":
    raise SystemExit(main())
