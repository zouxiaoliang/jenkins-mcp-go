package jenkins

import (
	"context"
	"fmt"
	"strings"
)

// Response bundle types returned to the tool layer. They live here so that the
// JSON an agent sees is defined next to the code that produces it.

// ConsoleResult is the answer to a console-log read: the text, where to resume,
// and an honest account of what was left out.
type ConsoleResult struct {
	Job   string `json:"job"`
	Build string `json:"build"`
	Text  string `json:"text"`
	Lines int    `json:"lines_returned"`
	// NextStart is the Byte Cursor to pass on the next call.
	NextStart int64 `json:"next_start"`
	// MoreData mirrors the controller's own "still writing" flag.
	MoreData bool `json:"more_data"`
	// TotalBytes is the whole log's current size, so a caller can tell how much
	// it has yet to read without downloading it.
	TotalBytes int64 `json:"total_bytes"`
	Truncated  bool  `json:"truncated"`
	// SpillPath points at the full log when the response had to be cut short.
	SpillPath string `json:"spill_path,omitempty"`
	// Note states any caveat (truncation, held-back partial line) in plain words.
	Note string `json:"note,omitempty"`
}

// ConsoleOptions controls a console read.
type ConsoleOptions struct {
	// TailLines, when > 0 and Cursor is 0, returns the last N lines instead of
	// the whole log.
	TailLines int
	// Cursor resumes from a previous NextStart.
	Cursor int64
	// SpillDir, when set, receives the full log if the response is truncated.
	SpillDir string
}

// ConsoleRead implements the console tool's semantics: resume from a cursor when
// given, otherwise hand back the tail.
func (c *Client) ConsoleRead(ctx context.Context, fullName string, ref BuildRef, opts ConsoleOptions) (*ConsoleResult, error) {
	res := &ConsoleResult{Job: strings.Trim(fullName, "/"), Build: string(ref)}
	var (
		text      string
		total     int64
		more      bool
		nextStart int64
		err       error
	)
	if opts.Cursor > 0 {
		var chunk *LogChunk
		chunk, err = c.ReadLog(ctx, fullName, ref, opts.Cursor)
		if err != nil {
			return nil, err
		}
		text, total, more, nextStart = chunk.Text, chunk.TotalSize, chunk.MoreData, chunk.NextStart
	} else {
		tail := opts.TailLines
		if tail <= 0 {
			tail = 200
		}
		text, total, more, err = c.TailLog(ctx, fullName, ref, tail)
		if err != nil {
			return nil, err
		}
		nextStart = total
	}
	res.Text = text
	res.Lines = CountLines(text)
	res.TotalBytes = total
	res.MoreData = more
	res.NextStart = nextStart
	if more {
		res.Note = "build is still running; call again with next_start to read what follows"
	}
	// A tail read is inherently partial. Say so instead of implying the log
	// starts where our window started.
	if opts.Cursor == 0 && total > int64(len(text)) {
		res.Truncated = true
		if res.Note == "" {
			res.Note = fmt.Sprintf("showing the last %d lines of a %d byte log", res.Lines, total)
		} else {
			res.Note += fmt.Sprintf("; showing the last %d lines of a %d byte log", res.Lines, total)
		}
		if opts.SpillDir != "" {
			if p, serr := c.SpillLog(ctx, fullName, ref, opts.SpillDir); serr == nil {
				res.SpillPath = p
			}
		}
	}
	return res, nil
}

// ---------------------------------------------------------------------------
// Escape Hatch support
// ---------------------------------------------------------------------------

// ValidateGetPath enforces the Escape Hatch's path policy: an absolute,
// same-origin path with no traversal. Anything else is refused before a request
// is built, so the tool cannot be turned into an SSRF primitive.
func ValidateGetPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", NewError(CodeInvalidParameter, "path is empty", "pass a controller path such as /api/json")
	}
	if !strings.HasPrefix(p, "/") {
		return "", NewError(CodeInvalidParameter, "path must start with /", p)
	}
	// A leading "//" is a protocol-relative URL: it names another host.
	if strings.HasPrefix(p, "//") {
		return "", NewError(CodeInvalidParameter, "path must not start with //", p)
	}
	if strings.Contains(p, "://") {
		return "", NewError(CodeInvalidParameter, "path must not contain a scheme", p)
	}
	for _, seg := range strings.Split(strings.SplitN(p, "?", 2)[0], "/") {
		if seg == ".." {
			return "", NewError(CodeInvalidParameter, "path must not contain ..", p)
		}
	}
	if strings.ContainsAny(p, "\r\n\x00") {
		return "", NewError(CodeInvalidParameter, "path contains a control character", "")
	}
	return p, nil
}

// Envelope wraps a body that may have been cut to size.
type Envelope struct {
	Body []byte
	// Truncated is true when Body is a prefix rather than the whole response.
	Truncated bool
	TotalSize int64
	Note      string
}

// Truncate applies a byte budget, refusing to split a UTF-8 sequence so that the
// caller never receives a broken character.
func Truncate(body []byte, limit int64) Envelope {
	env := Envelope{Body: body, TotalSize: int64(len(body))}
	if limit <= 0 || int64(len(body)) <= limit {
		return env
	}
	cut := limit
	// Back off to the start of the character that straddles the limit.
	for cut > 0 && cut < int64(len(body)) && body[cut]&0xC0 == 0x80 {
		cut--
	}
	env.Body = body[:cut]
	env.Truncated = true
	env.Note = fmt.Sprintf("response is %d bytes; returned the first %d", len(body), cut)
	return env
}
