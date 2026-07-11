package contracts

import (
	"bytes"
	"encoding/json"
	"errors"
)

// List preserves the API's list default over Go's nil-slice convention: a
// zero List marshals as [] rather than null.
type List[T any] []T

func (items List[T]) MarshalJSON() ([]byte, error) {
	if items == nil {
		return []byte("[]"), nil
	}
	return json.Marshal([]T(items))
}

func (items *List[T]) UnmarshalJSON(data []byte) error {
	if items == nil {
		return errors.New("cannot unmarshal List into nil receiver")
	}
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return errors.New("list cannot be null")
	}
	var decoded []T
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*items = decoded
	return nil
}
