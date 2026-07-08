"""时序交叉验证：walk-forward + purged K-fold + embargo（防泄漏）。

时序数据不能随机分折——训练必须在测试之前；标签有重叠/自相关时，还要剔除
测试集邻近的训练样本（purge + embargo），否则信息泄漏使 OOS 虚高。
参考：López de Prado《Advances in Financial ML》第 7 章。
"""

from __future__ import annotations


def walk_forward_splits(n: int, n_splits: int = 4, min_train: int | None = None,
                        anchored: bool = True) -> list[tuple[int, int, int, int]]:
    """滚动/锚定前移：返回 [(train_start, train_end, test_start, test_end), ...]。

    anchored=True 训练窗口从 0 起扩张；False 为固定长度滚动窗口。
    """
    fold = max(1, n // (n_splits + 1))
    min_train = min_train or fold
    splits: list[tuple[int, int, int, int]] = []
    for i in range(n_splits):
        test_start = min_train + i * fold
        if test_start >= n:
            break
        test_end = min(n, test_start + fold)
        train_start = 0 if anchored else max(0, test_start - min_train)
        splits.append((train_start, test_start, test_start, test_end))
    return splits


def purged_kfold_splits(n: int, k: int = 5, embargo: float = 0.01
                        ) -> list[tuple[list[int], list[int]]]:
    """Purged K-Fold：返回 [(train_indices, test_indices), ...]。

    每个测试折连续；训练集剔除测试折两侧 embargo 比例的样本，防标签重叠泄漏。
    """
    fold = max(1, n // k)
    emb = int(n * embargo)
    splits: list[tuple[list[int], list[int]]] = []
    for i in range(k):
        test_start = i * fold
        test_end = n if i == k - 1 else (i + 1) * fold
        if test_start >= n:
            break
        test = list(range(test_start, test_end))
        purge_lo = max(0, test_start - emb)
        purge_hi = min(n, test_end + emb)
        train = [j for j in range(n) if j < purge_lo or j >= purge_hi]
        splits.append((train, test))
    return splits
