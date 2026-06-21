package gomodstore

type PublishEvent struct {
	Event   string            `json:"event"`
	Module  string            `json:"module,omitempty"`
	Version string            `json:"version,omitempty"`
	Time    string            `json:"time,omitempty"`
	URLs    map[string]string `json:"urls,omitempty"`
	Error   string            `json:"error,omitempty"`
}
