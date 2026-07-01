package analyzer

import (
	"encoding/json"
	"strings"

	partialjson "github.com/blaze2305/partial-json-parser"
	partialopts "github.com/blaze2305/partial-json-parser/options"
)

// ExtractAllJSON locates every complete JSON object in the raw response.
func ExtractAllJSON(raw string) []string {
	var results []string
	raw = strings.TrimSpace(raw)

	search := raw
	for {
		idx := strings.Index(search, "{")
		if idx < 0 {
			break
		}
		sub := search[idx:]
		dec := json.NewDecoder(strings.NewReader(sub))
		var rm json.RawMessage
		if err := dec.Decode(&rm); err == nil {
			results = append(results, string(rm))
			search = search[idx+int(dec.InputOffset()):]
			continue
		} else {
			if repaired, ok := repairTruncatedJSON(sub); ok {
				var rm2 json.RawMessage
				if err := json.Unmarshal([]byte(repaired), &rm2); err == nil {
					results = append(results, string(rm2))
					search = search[idx+1:]
					continue
				}
			}
		}
		search = search[idx+1:]
	}

	var unique []string
	seen := make(map[string]bool)
	for _, res := range results {
		if !seen[res] {
			seen[res] = true
			unique = append(unique, res)
		}
	}
	return unique
}

// repairTruncatedJSON attempts to salvage a JSON object that was cut off.
func repairTruncatedJSON(raw string) (string, bool) {
	if strings.TrimSpace(raw) == "" {
		return raw, false
	}
	out, err := partialjson.ParseMalformedString(raw, partialopts.ALL, false)
	if err != nil || out == "" {
		return raw, false
	}
	return out, true
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
