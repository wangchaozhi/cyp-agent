package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/wangchaozhi/cyp-agent/internal/orders"
	"github.com/wangchaozhi/cyp-agent/internal/riskstate"
	runtimecore "github.com/wangchaozhi/cyp-agent/internal/runtime"
)

func (s *Server) reconcileNow(w http.ResponseWriter, request *http.Request) {
	if s.reconcile == nil {
		writeError(w, http.StatusNotImplemented, "运行时对账入口未配置")
		return
	}
	// Require a JSON object even though this operation currently has no knobs;
	// this keeps mutation CSRF/content-type handling uniform across the API.
	var payload map[string]any
	if err := decodeJSON(request, &payload); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if len(payload) != 0 {
		writeError(w, http.StatusUnprocessableEntity, "对账请求暂不接受参数")
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), 30*time.Second)
	defer cancel()
	report, err := s.reconcile(ctx)
	if err != nil {
		status := http.StatusServiceUnavailable
		if errors.Is(err, runtimecore.ErrReconcileInProgress) {
			status = http.StatusConflict
		}
		writeJSON(w, status, map[string]any{"detail": err.Error(), "report": report})
		return
	}
	writeJSON(w, http.StatusOK, report)
}

func (s *Server) orders(w http.ResponseWriter, request *http.Request) {
	values := s.orchestrator.Orders()
	if strings.EqualFold(request.URL.Query().Get("unresolved"), "true") {
		values = s.orchestrator.UnresolvedOrders()
	}
	limit, _ := strconv.Atoi(request.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	if len(values) > limit {
		values = values[:limit]
	}
	writeJSON(w, http.StatusOK, values)
}

type auditSnapshot struct {
	GeneratedAt time.Time                  `json:"generated_at"`
	Safety      runtimecore.SafetySnapshot `json:"safety"`
	Orders      []orders.Order             `json:"orders"`
	Trades      []riskstate.TradeRecord    `json:"trades"`
}

func (s *Server) auditExport(w http.ResponseWriter, _ *http.Request) {
	safety := runtimecore.SafetySnapshot{Frozen: true, Reason: "safety state is unavailable"}
	if s.safety != nil {
		safety = s.safety.Snapshot()
	}
	trades := []riskstate.TradeRecord{}
	if s.riskState != nil {
		trades = s.riskState.Trades()
	}
	w.Header().Set("Content-Disposition", `attachment; filename="cyp-audit.json"`)
	writeJSON(w, http.StatusOK, auditSnapshot{
		GeneratedAt: time.Now().UTC(), Safety: safety,
		Orders: s.orchestrator.Orders(), Trades: trades,
	})
}
