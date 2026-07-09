"""隔离签名器：签名能力与业务代码隔离，私钥永不出签名器边界。

安全约定：
- 私钥只存在于签名器实例内部，__repr__/__str__/日志一律脱敏。
- KeystoreSigner：本地加密 keystore（eth-account scrypt），口令从环境变量读。
- KMS / 硬件钱包：接口占位，签名请求外发、私钥不进本进程。
"""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any, Protocol, runtime_checkable


@runtime_checkable
class Signer(Protocol):
    @property
    def address(self) -> str: ...
    def sign_transaction(self, tx: dict[str, Any]) -> bytes: ...


class KeystoreSigner:
    """本地加密 keystore 签名器（web3 可选依赖：pip install .[onchain]）。"""

    def __init__(self, keystore_path: str, passphrase: str) -> None:
        try:
            from eth_account import Account
        except ImportError as e:  # pragma: no cover
            raise RuntimeError("需要 eth-account：pip install .[onchain]") from e
        raw = json.loads(Path(keystore_path).read_text(encoding="utf-8"))
        self._account = Account.from_key(Account.decrypt(raw, passphrase))
        # 立刻丢弃口令引用；私钥仅存在于 _account 内部

    @property
    def address(self) -> str:
        return self._account.address

    def sign_transaction(self, tx: dict[str, Any]) -> bytes:
        return self._account.sign_transaction(tx).raw_transaction

    def __repr__(self) -> str:  # 防止意外把私钥打进日志
        return f"KeystoreSigner(address={self.address})"

    __str__ = __repr__


class KmsSigner:
    """云 KMS 签名器占位：签名请求外发 KMS，私钥不进本进程。"""

    def __init__(self, key_id: str) -> None:
        self._key_id = key_id

    @property
    def address(self) -> str:
        raise NotImplementedError("KMS 签名器待接入（M3 后续）")

    def sign_transaction(self, tx: dict[str, Any]) -> bytes:
        raise NotImplementedError("KMS 签名器待接入（M3 后续）")

    def __repr__(self) -> str:
        return f"KmsSigner(key_id={self._key_id[:6]}***)"


class HardwareSigner:
    """硬件钱包签名器占位（Ledger/Trezor）。"""

    def __init__(self, derivation_path: str = "m/44'/60'/0'/0/0") -> None:
        self._path = derivation_path

    @property
    def address(self) -> str:
        raise NotImplementedError("硬件签名器待接入（M3 后续）")

    def sign_transaction(self, tx: dict[str, Any]) -> bytes:
        raise NotImplementedError("硬件签名器待接入（M3 后续）")


def build_signer(kind: str, **kwargs) -> Signer:
    if kind == "keystore":
        return KeystoreSigner(**kwargs)
    if kind == "kms":
        return KmsSigner(**kwargs)
    if kind == "hardware":
        return HardwareSigner(**kwargs)
    raise ValueError(f"未知签名器类型：{kind}")
