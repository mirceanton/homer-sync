package controller

import (
	_ "embed"
	"bytes"
	"fmt"
	"os"
	"text/template"
)

//go:embed default.tmpl
var defaultTemplate string

// TemplateData is the context passed to the Homer config template.
type TemplateData struct {
	Title    string
	Subtitle string
	Columns  int
	Groups   []GroupData
}

// GroupData represents one Homer service group with its sorted items.
type GroupData struct {
	Name  string
	Icon  string
	Items []ServiceItem
}

// renderConfig executes the Homer config template against data and returns the
// rendered YAML string.  When templatePath is non-empty and the file exists it
// is used as the template; otherwise the built-in default is used.
func renderConfig(data TemplateData, templatePath string) (string, error) {
	var src string

	if templatePath != "" {
		raw, err := os.ReadFile(templatePath)
		if err != nil {
			return "", fmt.Errorf("read custom template %q: %w", templatePath, err)
		}
		src = string(raw)
	} else {
		src = defaultTemplate
	}

	tmpl, err := template.New("homer").Parse(src)
	if err != nil {
		return "", fmt.Errorf("parse template: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("execute template: %w", err)
	}
	return buf.String(), nil
}
