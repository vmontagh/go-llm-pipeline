package llmvcr

import (
	"bytes"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"

	"gopkg.in/dnaeon/go-vcr.v3/cassette"

	"github.com/vmontagh/go-llm-pipeline/pkg/scrub"
)

// defaultScrubRules returns the built-in redaction rules applied to cassette
// bodies and remaining header values: bearer tokens, OpenAI secret keys, and
// Google API keys.
func defaultScrubRules() []scrub.Rule {
	return []scrub.Rule{
		scrub.MustRegexpRule(`(?i)(bearer\s+)[A-Za-z0-9\-._~+/]+=*`, "${1}"+redactedValue),
		scrub.MustRegexpRule(`sk-[A-Za-z0-9_\-]{20,}`, redactedValue),
		scrub.MustRegexpRule(`AIza[0-9A-Za-z_\-]{35}`, redactedValue),
	}
}

// DefaultScrubber returns a scrubber that redacts common LLM provider secrets
// (bearer tokens, OpenAI sk- keys, Google AIza keys) from any text it is applied
// to. Callers can extend it with additional scrub.Rules.
func DefaultScrubber() *scrub.Scrubber {
	return scrub.NewScrubber(defaultScrubRules()...)
}

// beforeSaveHook builds the go-vcr BeforeSave hook that redacts and formats an
// interaction before it is persisted.
func (c *config) beforeSaveHook() func(*cassette.Interaction) error {
	return func(i *cassette.Interaction) error {
		c.redactHeaderValues(i.Request.Headers)
		c.redactHeaderValues(i.Response.Headers)
		c.redactURLQuery(&i.Request.URL)
		c.scrubBody(&i.Request.Body, &i.Request.ContentLength, i.Request.Headers)
		c.scrubBody(&i.Response.Body, &i.Response.ContentLength, i.Response.Headers)

		if c.prettyJSON {
			prettyFormatBody(&i.Request.Body, i.Request.Headers, &i.Request.ContentLength)
			prettyFormatBody(&i.Response.Body, i.Response.Headers, &i.Response.ContentLength)
		}
		return nil
	}
}

// redactHeaderValues replaces the values of configured sensitive headers with
// the redaction placeholder, then scrubs any remaining header values.
func (c *config) redactHeaderValues(headers map[string][]string) {
	for _, name := range c.redactHeaders {
		for key := range headers {
			if strings.EqualFold(key, name) {
				for idx := range headers[key] {
					headers[key][idx] = redactedValue
				}
			}
		}
	}
	if c.scrubber == nil {
		return
	}
	for key := range headers {
		for idx := range headers[key] {
			headers[key][idx] = c.scrubber.Scrub(headers[key][idx])
		}
	}
}

// redactURLQuery replaces configured sensitive query parameters in the URL.
func (c *config) redactURLQuery(rawURL *string) {
	if rawURL == nil || len(c.redactQueryParams) == 0 {
		return
	}
	u, err := url.Parse(*rawURL)
	if err != nil {
		return
	}
	q := u.Query()
	modified := false
	for _, param := range c.redactQueryParams {
		if q.Has(param) {
			q.Set(param, redactedValue)
			modified = true
		}
	}
	if modified {
		u.RawQuery = q.Encode()
		*rawURL = u.String()
	}
}

// scrubBody applies the scrubber to a body and syncs its content length.
func (c *config) scrubBody(body *string, cl *int64, headers map[string][]string) {
	if c.scrubber == nil || body == nil || *body == "" {
		return
	}
	scrubbed := c.scrubber.Scrub(*body)
	if scrubbed == *body {
		return
	}
	*body = scrubbed
	setContentLength(headers, cl, len(scrubbed))
}

func prettyFormatBody(body *string, headers map[string][]string, cl *int64) {
	if body == nil || *body == "" || !hasJSONContentType(headers) {
		return
	}
	prettyBody, ok := prettyJSON(*body)
	if !ok {
		return
	}
	*body = prettyBody
	setContentLength(headers, cl, len(*body))
}

func hasJSONContentType(headers map[string][]string) bool {
	for k, v := range headers {
		if !strings.EqualFold(k, "Content-Type") {
			continue
		}
		if len(v) > 0 && strings.Contains(strings.ToLower(v[0]), "application/json") {
			return true
		}
	}
	return false
}

func prettyJSON(raw string) (string, bool) {
	var buf bytes.Buffer
	if err := json.Indent(&buf, []byte(raw), "", "  "); err != nil {
		return "", false
	}
	return buf.String(), true
}

func setContentLength(headers map[string][]string, cl *int64, length int) {
	if len(headers["Content-Length"]) > 0 {
		headers["Content-Length"][0] = strconv.Itoa(length)
	}
	if cl != nil {
		*cl = int64(length)
	}
}
