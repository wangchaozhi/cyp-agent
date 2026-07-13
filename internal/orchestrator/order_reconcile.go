package orchestrator

import (
	"context"
	"fmt"

	"github.com/wangchaozhi/cyp-agent/internal/contracts"
	"github.com/wangchaozhi/cyp-agent/internal/orders"
	"github.com/wangchaozhi/cyp-agent/internal/venue"
)

// ReconcileOrders consumes the durable journal without ever submitting a new
// order. Remote in-flight OKX Demo orders are looked up by client ID; Paper
// misses are authoritative because its venue state is process-local.
func (s *Service) ReconcileOrders(ctx context.Context, positions []contracts.Position) ([]string, error) {
	if ctx == nil {
		return nil, fmt.Errorf("order reconcile context is required")
	}
	positionExists := func(intent contracts.OrderIntent) bool {
		for _, position := range positions {
			if position.Symbol == intent.Symbol && position.Instrument == intent.Instrument {
				return true
			}
		}
		return false
	}
	discrepancies := make([]string, 0)
	for _, order := range s.journal.Orders() {
		if orders.IsTerminal(order.Status) {
			continue
		}
		if err := ctx.Err(); err != nil {
			return discrepancies, err
		}
		switch order.Status {
		case contracts.OrderStatusFilled, contracts.OrderStatusProtectivePlaced,
			contracts.OrderStatusProtectiveFailed, contracts.OrderStatusFlattening:
			if !positionExists(order.Intent) {
				if cleanupErr := s.clearProtectiveOrders(ctx, order.Intent.Symbol); cleanupErr != nil {
					discrepancies = append(discrepancies,
						fmt.Sprintf("订单 %s 空仓保护单清理失败：%s", order.ClientID, cleanupErr.Error()))
					continue
				}
				if err := s.reconcileOrderTransition(ctx, order, contracts.OrderStatusClosed, order.Result, "venue position is flat"); err != nil {
					return discrepancies, err
				}
				continue
			}
			if order.Status == contracts.OrderStatusFilled {
				next := contracts.OrderStatusProtectiveFailed
				if order.Result != nil && len(order.Result.ProtectiveOrders) > 0 {
					next = contracts.OrderStatusProtectivePlaced
				}
				if err := s.reconcileOrderTransition(ctx, order, next, nil, "restored filled order lifecycle"); err != nil {
					return discrepancies, err
				}
			}
			if order.Status == contracts.OrderStatusProtectiveFailed || order.Status == contracts.OrderStatusFlattening {
				discrepancies = append(discrepancies,
					fmt.Sprintf("订单 %s 仍处于 %s", order.ClientID, order.Status))
			}
			continue
		case contracts.OrderStatusNew:
			if err := s.reconcileOrderTransition(ctx, order, contracts.OrderStatusCanceled, nil, "process stopped before submission"); err != nil {
				return discrepancies, err
			}
			continue
		}

		reader, ok := s.venue.(venue.OrderReconciler)
		if !ok {
			discrepancies = append(discrepancies,
				fmt.Sprintf("订单 %s 无可用的远端状态查询能力", order.ClientID))
			continue
		}
		result, found, err := reader.ReconcileOrder(ctx, order.Intent)
		if err != nil {
			discrepancies = append(discrepancies,
				fmt.Sprintf("订单 %s 查询失败：%s", order.ClientID, err.Error()))
			continue
		}
		if !found {
			if s.venue.Kind() == venue.KindPaper {
				if err := s.reconcileOrderTransition(ctx, order, contracts.OrderStatusCanceled, nil, "paper venue confirms order absent"); err != nil {
					return discrepancies, err
				}
				continue
			}
			discrepancies = append(discrepancies,
				fmt.Sprintf("订单 %s 在远端暂未找到，禁止盲目重试", order.ClientID))
			continue
		}
		if result.Status == order.Status {
			discrepancies = append(discrepancies,
				fmt.Sprintf("订单 %s 仍处于远端状态 %s", order.ClientID, result.Status))
			continue
		}
		if !orders.CanTransition(order.Status, result.Status) {
			discrepancies = append(discrepancies,
				fmt.Sprintf("订单 %s 状态无法从 %s 对账为 %s", order.ClientID, order.Status, result.Status))
			continue
		}
		if err := s.reconcileOrderTransition(ctx, order, result.Status, &result, "authoritative venue lookup"); err != nil {
			return discrepancies, err
		}
		if result.Status == contracts.OrderStatusAcknowledged || result.Status == contracts.OrderStatusPartiallyFilled {
			discrepancies = append(discrepancies,
				fmt.Sprintf("订单 %s 仍处于远端状态 %s", order.ClientID, result.Status))
			continue
		}
		if result.Status == contracts.OrderStatusFilled {
			next := contracts.OrderStatusProtectiveFailed
			if len(result.ProtectiveOrders) > 0 {
				next = contracts.OrderStatusProtectivePlaced
			}
			updated, _ := s.journal.Get(order.ClientID)
			if err := s.reconcileOrderTransition(ctx, updated, next, nil, "restored protection lifecycle"); err != nil {
				return discrepancies, err
			}
		}
	}
	return discrepancies, nil
}

func (s *Service) reconcileOrderTransition(
	ctx context.Context,
	order orders.Order,
	status contracts.OrderStatus,
	result *contracts.ExecutionResult,
	note string,
) error {
	eventID := "reconcile:" + order.ClientID + ":" + string(status)
	if err := s.journal.TransitionContext(ctx, eventID, order.ClientID, status, result, note); err != nil {
		s.freezeDurability("order reconciliation journal persistence failed")
		return fmt.Errorf("persist order %s reconciliation: %w", order.ClientID, err)
	}
	return nil
}
