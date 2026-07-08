"""端到端最小闭环示例。

    python -m cyp.examples.run --symbol BTC/USDT --approve auto     # 离线自动演示
    python -m cyp.examples.run --approve cli                        # 人工审批

等价于 `python -m cyp.cli`，见 cyp/cli.py。
"""

from cyp.cli import main

if __name__ == "__main__":
    raise SystemExit(main())
