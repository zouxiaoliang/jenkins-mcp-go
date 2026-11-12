package jenkins

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// maxBodyBytes bounds any single controller response we decode. The controller
// can return multi-megabyte JSON for a bare api/json call; anything past this is
// a mistake in our tree filter, not data an agent wants.
const maxBodyBytes = 32 << 20 // 32 MiB

func readAll(resp *http.Response) (string, error) {
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return "", classifyTransport(err)
	}
	return string(b), nil
}

func jsonUnmarshal(b []byte, v any) error {
	return json.Unmarshal(b, v)
}

// paramTypeAliases maps the plugin-provided _class to the short type name agents
// see. Unknown classes keep their `type` field so that a new plugin surfaces as
// its own name rather than as a wrong one.
func normalizeParamType(class, typ string) string {
	switch {
	case strings.HasSuffix(class, "ExtendedChoiceParameterDefinition"):
		return "ExtendedChoice"
	case strings.HasSuffix(class, "ListSubversionTagsParameterDefinition"):
		return "ListSubversionTags"
	case strings.HasSuffix(class, "ParameterSeparatorDefinition"):
		return "Separator"
	case strings.HasSuffix(class, "BooleanParameterDefinition"):
		return "Boolean"
	case strings.HasSuffix(class, "ChoiceParameterDefinition"):
		return "Choice"
	case strings.HasSuffix(class, "PasswordParameterDefinition"):
		return "Password"
	case strings.HasSuffix(class, "FileParameterDefinition"):
		return "File"
	case strings.HasSuffix(class, "TextParameterDefinition"):
		return "Text"
	case strings.HasSuffix(class, "StringParameterDefinition"):
		return "String"
	}
	if typ != "" {
		return typ
	}
	if i := strings.LastIndex(class, "."); i >= 0 {
		return class[i+1:]
	}
	return class
}

// isSeparatorType reports whether a parameter definition is a visual separator
// rather than an input.
func isSeparatorType(class, typ string) bool {
	return strings.HasSuffix(class, "ParameterSeparatorDefinition") ||
		strings.EqualFold(typ, "ParameterSeparatorDefinition") ||
		strings.EqualFold(typ, "Separator")
}
