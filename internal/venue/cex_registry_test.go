package venue

import (
	"testing"

	"github.com/wangchaozhi/cyp-agent/internal/contracts"
)

func TestVenueRegistryDescribesAndReplacesByID(t *testing.T) {
	paper := NewPaperVenue()
	binance, err := NewBinanceVenue(CEXConfig{BaseURL: "http://example.test"})
	if err != nil {
		t.Fatal(err)
	}
	registry := NewVenueRegistry(paper, binance)
	if got := registry.IDs(); len(got) != 2 || got[0] != "binance" || got[1] != "paper" {
		t.Fatalf("ids=%v", got)
	}
	description := registry.Describe()
	if len(description) != 2 || description[0].ID != "paper" || description[1].ID != "binance" ||
		!description[1].ReadOnly {
		t.Fatalf("description=%#v", description)
	}
	replacement, _ := NewBinanceVenue(CEXConfig{
		BaseURL: "http://replacement.test", EstimatedSlippageBPS: contracts.MustDecimal("20"),
	})
	registry.Register(replacement)
	if len(registry.All()) != 2 {
		t.Fatal("replacement duplicated registry order")
	}
	current, ok := registry.Get("binance")
	if !ok || current != replacement {
		t.Fatal("replacement was not installed")
	}
}

func TestExecutionIdentitySeparatesPaperDemoAndLive(t *testing.T) {
	paper := IdentifyExecution(NewPaperVenue())
	if paper.Environment != EnvironmentPaper || !paper.Writable {
		t.Fatalf("paper identity=%+v", paper)
	}
	demo, err := NewOKXVenue(CEXConfig{
		Demo: true, EnableDemoTrading: true,
		APIKey: "demo-key", APISecret: "demo-secret", Passphrase: "demo-passphrase",
	})
	if err != nil {
		t.Fatal(err)
	}
	demoIdentity := IdentifyExecution(demo)
	if demoIdentity.Environment != EnvironmentDemo || !demoIdentity.Writable {
		t.Fatalf("demo identity=%+v", demoIdentity)
	}
	live, err := NewBinanceVenue(CEXConfig{})
	if err != nil {
		t.Fatal(err)
	}
	liveIdentity := IdentifyExecution(live)
	if liveIdentity.Environment != EnvironmentLive || liveIdentity.Writable {
		t.Fatalf("live identity=%+v", liveIdentity)
	}
}
