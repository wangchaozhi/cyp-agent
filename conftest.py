"""让 `import cyp` 在未 `pip install -e .` 时也能工作（把 core/ 加入路径）。"""

import pathlib
import sys

sys.path.insert(0, str(pathlib.Path(__file__).parent / "core"))
