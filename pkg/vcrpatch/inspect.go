package vcrpatch

import (
	"fmt"
	"io"
	"text/template"
)

const (
	maxURLWidth     = 80
	truncateSuffix  = "..."
	truncatePadding = 3
)

const inspectTemplateText = `--- {{ .Path }} (Interactions: {{ len .Cassette.Interactions }}) ---
{{- range $i, $int := .Cassette.Interactions }}
[{{ printf "%2d" $i }}] {{ printf "%-6s" $int.Request.Method }} {{ printf "%-80s" (truncateURL $int.Request.URL) }} -> {{ printf "%3d" $int.Response.Code }} (ReqBody: {{ printf "%5d" (len $int.Request.Body) }} bytes, RespBody: {{ printf "%5d" (len $int.Response.Body) }} bytes)
{{- end }}
`

var inspectTemplate = template.Must(template.New("inspect").Funcs(template.FuncMap{
	"truncateURL": func(u string) string {
		if len(u) > maxURLWidth {
			return u[:maxURLWidth-truncatePadding] + truncateSuffix
		}
		return u
	},
}).Parse(inspectTemplateText))

// InspectCassette prints a summary of a cassette's interactions to an io.Writer.
func InspectCassette(w io.Writer, path string) error {
	c, err := LoadCassette(path)
	if err != nil {
		return fmt.Errorf("loading %s: %w", path, err)
	}

	data := struct {
		Path     string
		Cassette *Cassette
	}{
		Path:     path,
		Cassette: c,
	}

	return inspectTemplate.Execute(w, data)
}
