package render

import "text/template"

type MockRenderData struct {
	d map[string]interface{}
	f template.FuncMap
}

func (m *MockRenderData) Funcs() template.FuncMap {
	return m.f
}

func (m *MockRenderData) Data() map[string]interface{} {
	return m.d
}

func (m *MockRenderData) setKey(k, v string) {
	if m.d == nil {
		m.d = map[string]interface{}{}
	}

	m.d[k] = v
}

func (m *MockRenderData) setFunc(k string, v interface{}) {
	if m.f == nil {
		m.f = template.FuncMap{}
	}
	m.f[k] = v
}
