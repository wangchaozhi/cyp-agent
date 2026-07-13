package orchestrator

import (
	"context"
	"testing"

	"github.com/wangchaozhi/cyp-agent/internal/contracts"
	"github.com/wangchaozhi/cyp-agent/internal/orders"
	runtimecore "github.com/wangchaozhi/cyp-agent/internal/runtime"
	"github.com/wangchaozhi/cyp-agent/internal/venue"
)

func TestOrderReconcileClosesStalePaperLifecycleAndCancelsUnsubmitted(t *testing.T) {
	journal := orders.NewJournal()
	openIntent := contracts.OrderIntent{
		ClientID: "open-order", Symbol: "BTC/USDT", Venue: "paper",
		Side: contracts.SideLong, Instrument: contracts.InstrumentSpot,
		OrderType: contracts.EntryTypeMarket, SizeQuote: contracts.MustDecimal("100"),
	}
	if err := journal.Open("open", openIntent); err != nil {
		t.Fatal(err)
	}
	if err := journal.Transition("submit", openIntent.ClientID, contracts.OrderStatusSubmitting, nil, ""); err != nil {
		t.Fatal(err)
	}
	fill := &contracts.ExecutionResult{
		ClientID: openIntent.ClientID, Status: contracts.OrderStatusFilled,
		ProtectiveOrders: contracts.List[contracts.ProtectiveOrder]{
			{Kind: "stop_loss", OrderID: "sl", TriggerPrice: contracts.MustDecimal("90"), ReduceOnly: true},
		},
	}
	if err := journal.Transition("fill", openIntent.ClientID, contracts.OrderStatusFilled, fill, ""); err != nil {
		t.Fatal(err)
	}
	if err := journal.Transition("protect", openIntent.ClientID, contracts.OrderStatusProtectivePlaced, nil, ""); err != nil {
		t.Fatal(err)
	}
	pendingIntent := openIntent
	pendingIntent.ClientID = "never-submitted"
	if err := journal.Open("pending-open", pendingIntent); err != nil {
		t.Fatal(err)
	}

	service := &Service{
		venue: venue.NewPaperVenue(), journal: journal,
		safety: runtimecore.NewSafetyState(),
	}
	discrepancies, err := service.ReconcileOrders(context.Background(), nil)
	if err != nil || len(discrepancies) != 0 {
		t.Fatalf("reconcile discrepancies=%v err=%v", discrepancies, err)
	}
	openOrder, _ := journal.Get(openIntent.ClientID)
	pendingOrder, _ := journal.Get(pendingIntent.ClientID)
	if openOrder.Status != contracts.OrderStatusClosed || pendingOrder.Status != contracts.OrderStatusCanceled {
		t.Fatalf("reconciled orders open=%s pending=%s", openOrder.Status, pendingOrder.Status)
	}
	if len(journal.Unresolved()) != 0 {
		t.Fatalf("orders remain unresolved: %+v", journal.Unresolved())
	}
}
