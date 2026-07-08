"""时序交叉验证 + PBO 过拟合概率。纯 Python 确定性。"""

import random

from cyp.backtest.pbo import pbo
from cyp.backtest.validate import purged_kfold_splits, walk_forward_splits


# ---- walk-forward ----------------------------------------------------------

def test_walk_forward_train_precedes_test():
    splits = walk_forward_splits(100, n_splits=4)
    assert len(splits) == 4
    for tr_s, tr_e, te_s, te_e in splits:
        assert tr_s == 0 and tr_e == te_s and te_s < te_e     # 锚定、训练在测试前、不重叠


def test_walk_forward_rolling_window():
    splits = walk_forward_splits(100, n_splits=3, anchored=False)
    for tr_s, tr_e, te_s, te_e in splits:
        assert tr_e == te_s and tr_s >= 0


# ---- purged K-fold ---------------------------------------------------------

def test_purged_kfold_test_folds_disjoint_and_cover():
    splits = purged_kfold_splits(100, k=5, embargo=0.0)
    all_test = [i for _, test in splits for i in test]
    assert sorted(all_test) == list(range(100))               # 覆盖且不漏
    for _, test in splits:
        assert len(set(test)) == len(test)                    # 折内不重


def test_embargo_purges_neighbors_from_train():
    splits = purged_kfold_splits(100, k=5, embargo=0.05)      # emb=5
    train, test = splits[2]                                    # 中间折 [40,60)
    # 训练集不应包含测试折两侧 embargo 区 [35,65)
    assert all(not (35 <= j < 65) for j in train)
    assert 30 in train and 70 in train                        # 区外仍在


# ---- PBO -------------------------------------------------------------------

def test_pbo_zero_for_dominant_strategy():
    mean = lambda r: sum(r) / len(r)
    dominant = [0.02] * 60                                     # 恒最高
    weak1 = [0.001] * 60
    weak2 = [-0.01] * 60
    # 用均值作度量（恒定序列夏普为 0，均值可区分）
    assert pbo([dominant, weak1, weak2], s=4, metric=mean) == 0.0


def test_pbo_in_unit_interval_for_noise():
    rng = random.Random(42)
    strategies = [[rng.gauss(0, 0.01) for _ in range(120)] for _ in range(6)]
    val = pbo(strategies, s=6)
    assert 0.0 <= val <= 1.0                                  # 纯噪声 → PBO 有意义地非零
