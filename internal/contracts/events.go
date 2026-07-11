package contracts

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// DashboardEvent represents the versioned REST/SSE wire contract. Data is
// flattened into the top-level object so new event-specific fields can be
// introduced without changing the stable type/run_id/ts envelope.
type DashboardEvent struct {
	Type  string
	RunID string
	TS    time.Time
	Data  map[string]any
}

func (event DashboardEvent) MarshalJSON() ([]byte, error) {
	payload := make(map[string]any, len(event.Data)+3)
	for key, value := range event.Data {
		if key == "type" || key == "run_id" || key == "ts" {
			continue
		}
		payload[key] = value
	}
	payload["type"] = event.Type
	payload["run_id"] = event.RunID
	payload["ts"] = event.TS
	return json.Marshal(payload)
}

func (event *DashboardEvent) UnmarshalJSON(data []byte) error {
	if event == nil {
		return errors.New("cannot unmarshal DashboardEvent into nil receiver")
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	var decoded DashboardEvent
	if value, ok := raw["type"]; !ok {
		return errors.New("dashboard event type is required")
	} else if err := json.Unmarshal(value, &decoded.Type); err != nil {
		return fmt.Errorf("dashboard event type: %w", err)
	}
	if value, ok := raw["run_id"]; !ok {
		return errors.New("dashboard event run_id is required")
	} else if err := json.Unmarshal(value, &decoded.RunID); err != nil {
		return fmt.Errorf("dashboard event run_id: %w", err)
	}
	if value, ok := raw["ts"]; !ok {
		return errors.New("dashboard event ts is required")
	} else if err := json.Unmarshal(value, &decoded.TS); err != nil {
		return fmt.Errorf("dashboard event ts: %w", err)
	}
	decoded.Data = make(map[string]any, len(raw)-3)
	for key, value := range raw {
		if key == "type" || key == "run_id" || key == "ts" {
			continue
		}
		decoder := json.NewDecoder(bytes.NewReader(value))
		decoder.UseNumber()
		var field any
		if err := decoder.Decode(&field); err != nil {
			return fmt.Errorf("dashboard event field %s: %w", key, err)
		}
		decoded.Data[key] = field
	}
	*event = decoded
	return nil
}
