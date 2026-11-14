package completion

import "calcside/internal/opdoc"

type Symbol struct {
	Name      string           `json:"name"`
	Kind      string           `json:"kind"`
	Detail    string           `json:"detail,omitempty"`
	Doc       string           `json:"doc,omitempty"`
	Params    []string         `json:"params,omitempty"`
	ParamDocs []opdoc.ParamDoc `json:"param_docs,omitempty"`
}

type Context struct {
	Symbols   []Symbol `json:"symbols"`
	EnvKeys   []string `json:"env_keys"`
	Truncated bool     `json:"truncated"`
}
