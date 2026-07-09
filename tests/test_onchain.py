"""M3 链上：OnchainVenue（mock EVM client）+ §2.3 风控规则 + nonce 对账。离线确定性。"""

import asyncio
from decimal import Decimal

from cyp.config import RiskConfig
from cyp.contracts import OrderIntent, TradeProposal
from cyp.risk import assess
from cyp.risk.rules import (
    RiskContext,
    RuleAction,
    rule_contract_whitelist,
    rule_gas_cap,
    rule_infinite_approval,
    rule_mev_route,
    rule_min_pool_tvl,
)
from cyp.runtime.reconcile import Reconciler
from cyp.venue import OnchainVenue

run = asyncio.run
CFG = RiskConfig(_env_file=None, contract_whitelist="0xrouter",
                 min_pool_tvl=Decimal("1000000"), max_gas_quote=Decimal("20"))


class MockEvmClient:
    """按 OnchainVenue 的 client 协议实现的确定性假链。"""

    def __init__(self, price="2000", price_impact="0.002", pool_tvl="5000000",
                 gas_quote="3", mev_protected=True, revert_swap=False):
        self.price = Decimal(price)
        self.price_impact = Decimal(price_impact)
        self.pool_tvl = Decimal(pool_tvl)
        self.gas_quote = Decimal(gas_quote)
        self.mev_protected = mev_protected
        self.revert_swap = revert_swap
        self.chain_nonce = 7
        self._allowances: dict[str, Decimal] = {}
        self.sent: list[tuple] = []                  # (kind, nonce, tx_hash)
        self._seq = 0

    def _tx(self, kind, nonce):
        self._seq += 1
        h = f"0xtx{self._seq}"
        self.sent.append((kind, nonce, h))
        return h

    async def get_nonce(self, address):
        return self.chain_nonce

    async def quote_swap(self, symbol, side, size_quote):
        return {"price": self.price, "price_impact": self.price_impact,
                "pool_tvl_usd": self.pool_tvl, "router": "0xrouter",
                "gas_quote": self.gas_quote, "mev_protected": self.mev_protected}

    async def allowance(self, symbol, router):
        return self._allowances.get(symbol, Decimal(0))

    async def send_approve(self, symbol, router, amount, nonce):
        self._allowances[symbol] = amount
        return self._tx("approve", nonce)

    async def send_swap(self, symbol, side, size_quote, min_out, nonce):
        return self._tx("swap", nonce)

    async def wait_receipt(self, tx_hash):
        kind = next(k for k, _, h in self.sent if h == tx_hash)
        status = 0 if (self.revert_swap and kind == "swap") else 1
        return {"status": status, "gas_used_quote": self.gas_quote}


def _venue(client=None):
    return OnchainVenue(chain="ethereum", client=client or MockEvmClient(),
                        initial_quote=Decimal("10000"))


def _intent(size="1000", client_id="oc1", side="long", reduce_only=False):
    return OrderIntent(client_id=client_id, symbol="ETH/USDC", venue="onchain-ethereum",
                       side=side, size_quote=Decimal(size), reduce_only=reduce_only,
                       stop_loss=Decimal("1800"))


# ---- OnchainVenue ------------------------------------------------------------

def test_place_does_exact_approve_then_swap():
    client = MockEvmClient()
    v = _venue(client)
    res = run(v.place(_intent()))
    assert res.status == "filled" and res.tx_hash and res.chain == "ethereum"
    kinds = [k for k, _, _ in client.sent]
    assert kinds == ["approve", "swap"]              # 两步：先精确授权再 swap
    assert client._allowances["ETH/USDC"] == Decimal("1000")   # 精确额度，非无限
    nonces = [n for _, n, _ in client.sent]
    assert nonces == [7, 8]                           # nonce 连续递增
    assert len(run(v.positions())) == 1


def test_place_skips_approve_when_allowance_enough():
    client = MockEvmClient()
    client._allowances["ETH/USDC"] = Decimal("5000")
    v = _venue(client)
    res = run(v.place(_intent()))
    assert res.status == "filled"
    assert [k for k, _, _ in client.sent] == ["swap"]


def test_place_idempotent_by_client_id():
    client = MockEvmClient()
    v = _venue(client)
    r1 = run(v.place(_intent()))
    r2 = run(v.place(_intent()))                     # 重放同一 client_id
    assert r1 is r2
    assert len([k for k, _, _ in client.sent if k == "swap"]) == 1   # 不重复上链


def test_swap_revert_handled():
    v = _venue(MockEvmClient(revert_swap=True))
    res = run(v.place(_intent()))
    assert res.status == "failed" and "revert" in res.error
    assert run(v.positions()) == []                  # revert 不记仓


def test_close_position_roundtrip():
    v = _venue()
    run(v.place(_intent()))
    res = run(v.place(_intent(client_id="oc2", reduce_only=True)))
    assert res.status == "filled"
    assert run(v.positions()) == []


def test_reconcile_onchain_aligns_nonce():
    client = MockEvmClient()
    v = _venue(client)
    run(v.place(_intent()))                          # 本地 nonce 走到 9
    client.chain_nonce = 12                          # 模拟链上有外部交易
    rep = run(v.reconcile_onchain())
    assert rep["nonce"] == 12
    assert any("nonce" in d for d in rep["discrepancies"])


def test_reconciler_integrates_onchain_hook():
    v = _venue()
    run(v.place(_intent()))
    report = run(Reconciler(v).reconcile())
    assert report.ok
    assert any("保护依赖监控存活" in g for g in report.protective_gaps)


# ---- §2.3 风控规则 -------------------------------------------------------------

def _prop(size="1000"):
    return TradeProposal(symbol="ETH/USDC", venue="onchain-ethereum", side="long",
                         size_quote=Decimal(size), stop_loss=Decimal("1800"), confidence=0.6)


def _ctx(**kw):
    base = {"equity_quote": Decimal("10000"), "ref_price": Decimal("2000"), "onchain": True,
            "contract_address": "0xrouter", "pool_tvl_usd": Decimal("5000000"),
            "est_gas_quote": Decimal("3"), "mev_protected": True}
    base.update(kw)
    return RiskContext(**base)


def test_rejects_unlimited_approval():
    r = rule_infinite_approval(_prop(), _ctx(approval_unlimited=True), CFG)
    assert r.action == RuleAction.REJECT


def test_rejects_oversized_approval():
    r = rule_infinite_approval(_prop("1000"), _ctx(approval_amount=Decimal("999999")), CFG)
    assert r.action == RuleAction.REJECT
    ok = rule_infinite_approval(_prop("1000"), _ctx(approval_amount=Decimal("1000")), CFG)
    assert ok.action == RuleAction.OK


def test_rejects_non_whitelisted_contract():
    r = rule_contract_whitelist(_prop(), _ctx(contract_address="0xevil"), CFG)
    assert r.action == RuleAction.REJECT
    assert rule_contract_whitelist(_prop(), _ctx(), CFG).action == RuleAction.OK


def test_rejects_low_tvl_pool():
    r = rule_min_pool_tvl(_prop(), _ctx(pool_tvl_usd=Decimal("50000")), CFG)
    assert r.action == RuleAction.REJECT


def test_rejects_excess_gas():
    r = rule_gas_cap(_prop(), _ctx(est_gas_quote=Decimal("50")), CFG)
    assert r.action == RuleAction.REJECT


def test_rejects_unprotected_mempool():
    r = rule_mev_route(_prop(), _ctx(mev_protected=False), CFG)
    assert r.action == RuleAction.REJECT


def test_onchain_rules_skip_for_cex_proposals():
    ctx = RiskContext(equity_quote=Decimal("10000"), ref_price=Decimal("2000"))  # onchain=False
    for rule in (rule_infinite_approval, rule_contract_whitelist, rule_min_pool_tvl, rule_mev_route):
        assert rule(_prop(), ctx, CFG).action == RuleAction.OK


def test_engine_assess_rejects_onchain_violations():
    ctx = _ctx(contract_address="0xevil", mev_protected=False)
    out = assess(_prop(), ctx, CFG)
    assert out.verdict == "rejected"
    assert any("contract_whitelist" in v for v in out.hard_violations)
    assert any("mev_route" in v for v in out.hard_violations)


# ---- 签名器 + 数据源 stub -------------------------------------------------------

def test_signer_repr_never_leaks_secret():
    from cyp.onchain import KmsSigner
    s = KmsSigner("supersecret-key-id-12345")
    assert "supersecret-key-id-12345" not in repr(s)


def test_build_signer_rejects_unknown_kind():
    from cyp.onchain import build_signer
    try:
        build_signer("magic")
        raise AssertionError("应抛 ValueError")
    except ValueError:
        pass


def test_onchain_data_source_degrades_without_fetcher():
    from cyp.data import OnchainDataSource
    src = OnchainDataSource()
    assert not src.is_configured()
    assert run(src.fetch("ETH/USDC")) is None


def test_onchain_data_source_with_fetcher():
    from cyp.data import OnchainDataSource

    async def fetcher(symbol):
        return {"smart_money_flow": Decimal("120000"), "liquidity_usd": Decimal("5000000")}

    data = run(OnchainDataSource(fetcher).fetch("ETH/USDC"))
    assert data is not None and data.smart_money_flow == Decimal("120000")
