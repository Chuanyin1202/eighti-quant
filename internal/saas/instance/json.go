package instance

import "encoding/json"

// Thin wrappers so champion.go doesn't pull "encoding/json" into its top
// imports — keeps the diff smaller across files and gives us a single
// point to swap to a different encoder later (msgpack etc.) if cache size
// becomes a concern.

func jsonMarshal(v any) ([]byte, error)       { return json.Marshal(v) }
func jsonUnmarshal(b []byte, v any) error     { return json.Unmarshal(b, v) }
