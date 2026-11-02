package server

import (
	"encoding/json"
	"net/http"

	"github.com/silverx/freellm-router/pkg/router"
)

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }
func jsonMarshal(v any) ([]byte, error)   { return json.Marshal(v) }

// contentString normalizes a JSON content field (string | array | null) to string.
func contentString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	if len(raw) > 0 && raw[0] == '[' {
		return string(raw)
	}
	var v any
	if err := json.Unmarshal(raw, &v); err == nil {
		out, _ := json.Marshal(v)
		return string(out)
	}
	return ""
}

// toolChoiceString converts a raw tool_choice into the string form accepted
// by router.RouteOpts ("auto" | "none" | "required" | "function:<name>").
func toolChoiceString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var o struct {
		Type     string `json:"type"`
		Function *struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if err := json.Unmarshal(raw, &o); err == nil {
		if o.Function != nil && o.Function.Name != "" {
			return "function:" + o.Function.Name
		}
		return o.Type
	}
	return ""
}

// statusFromError maps a routing failure to an HTTP status + error type.
func statusFromError(err error) (int, string) {
	if ue, ok := router.AsUpstreamErr(err); ok {
		switch {
		case ue.Status == http.StatusTooManyRequests || ue.Status == http.StatusRequestTimeout:
			return http.StatusTooManyRequests, "upstream_rate_limited"
		case ue.Status >= 500:
			return http.StatusBadGateway, "upstream_error"
		case ue.Status >= 400:
			return http.StatusBadGateway, "upstream_error"
		}
	}
	return http.StatusBadGateway, "upstream_error"
}

func errToMsg(err error) string {
	if ue, ok := router.AsUpstreamErr(err); ok {
		return ue.Msg
	}
	return err.Error()
}
