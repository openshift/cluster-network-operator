package render

import "text/template"

// RenderData is the interface all template data objects must implement
// This may not be necessary
type RenderData interface {
	Funcs() template.FuncMap

	Data() map[string]interface{}
}
