package webrtc

import (
	"encoding/json"
	"strconv"
)

func parseParticipantID(v interface{}) (int64, bool) {
	switch x := v.(type) {
	case float64:
		return int64(x), true
	case json.Number:
		n, err := x.Int64()
		return n, err == nil
	case string:
		n, err := strconv.ParseInt(x, 10, 64)
		return n, err == nil
	case int64:
		return x, true
	}
	return 0, false
}
