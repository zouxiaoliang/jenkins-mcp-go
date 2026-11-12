package mcpserver

import (
	"encoding/json"

	"github.com/zouxiaoliang/jenkins-mcp-go/internal/jenkins"
)

// toolError renders the bridge's error contract as the tool result text while
// keeping IsError=true on the wire.
//
// The SDK packs a returned error into the result's text content, so that text is
// the only place a structured error can live. Emitting the contract as JSON keeps
// `code` and `retryable` machine-readable instead of burying them in prose.
type toolError struct{ contract *jenkins.Error }

func (t toolError) Error() string {
	c := t.contract
	if c == nil {
		c = jenkins.NewError(jenkins.CodeInternal, "unspecified failure", "")
	}
	b, err := json.Marshal(struct {
		Error *jenkins.Error `json:"error"`
	}{c})
	if err != nil {
		// Marshalling a plain struct of strings and a bool cannot fail; if it
		// somehow does, still return valid JSON so the agent's parser survives.
		return `{"error":{"code":"internal","message":"unrenderable error","retryable":false}}`
	}
	return string(b)
}

// fail converts any error into the contract form used by tool results.
func fail(err error) error {
	if err == nil {
		return nil
	}
	return toolError{contract: jenkins.AsError(err)}
}
