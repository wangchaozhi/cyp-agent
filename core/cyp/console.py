"""Console helpers shared by CLI entry points."""

from __future__ import annotations

import sys


def configure_utf8_stdio() -> None:
    """Use UTF-8 when the host streams support runtime reconfiguration.

    Redirected/test streams do not necessarily expose ``reconfigure``.  CLI
    startup must remain best-effort so importing or embedding cyp never fails.
    """
    for stream in (sys.stdout, sys.stderr):
        reconfigure = getattr(stream, "reconfigure", None)
        if not callable(reconfigure):
            continue
        try:
            reconfigure(encoding="utf-8")
        except (AttributeError, OSError, ValueError):
            continue
