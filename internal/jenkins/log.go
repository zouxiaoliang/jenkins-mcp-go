package jenkins

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// tailWindow bounds how much of a log we fetch when the caller wants the tail.
// A controller can hold hundreds of megabytes of log; reading the head to find
// the tail would be pure waste.
const tailWindow = 512 << 10 // 512 KiB

// spillMax bounds the log we are willing to copy to disk when truncating.
const spillMax = 32 << 20 // 32 MiB

// LogChunk is a slice of a build's console log, always beginning at a line
// boundary.
type LogChunk struct {
	Text string
	// NextStart is the byte offset to pass as the cursor on the next read. It is
	// always at a line boundary, so concatenating chunks never splits a line.
	NextStart int64
	// MoreData reports whether the build is still writing to the log.
	MoreData bool
	// TotalSize is the controller's current size of the whole log, in bytes.
	TotalSize int64
}

type logHeaders struct {
	total int64
	more  bool
}

func parseLogHeaders(h http.Header) logHeaders {
	var lh logHeaders
	if v := h.Get("X-Text-Size"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			lh.total = n
		}
	}
	// Jenkins only sends X-More-Data when there IS more: a finished build omits
	// the header entirely (verified against 2.492.3). Defaulting to true made a
	// completed build look like it was still writing, so an agent would poll a
	// log that never grows again.
	lh.more = strings.EqualFold(h.Get("X-More-Data"), "true")
	return lh
}

func progressivePath(fullName string, ref BuildRef, start int64) (string, error) {
	bp, err := buildPath(fullName, ref)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s/logText/progressiveText?start=%d", bp, start), nil
}

// LogStat reports the current size of a log and whether it is still growing,
// without downloading it. The body is abandoned as soon as the headers arrive.
func (c *Client) LogStat(ctx context.Context, fullName string, ref BuildRef) (int64, bool, error) {
	path, err := progressivePath(fullName, ref, 0)
	if err != nil {
		return 0, false, err
	}
	reqCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := c.newRequest(reqCtx, http.MethodGet, path, nil)
	if err != nil {
		return 0, false, NewError(CodeInternal, "could not build request", err.Error())
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, false, classifyTransport(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return 0, false, classifyHTTP(resp.StatusCode, string(body))
	}
	lh := parseLogHeaders(resp.Header)
	// Abort the transfer now that the sizes are known.
	cancel()
	return lh.total, lh.more, nil
}

// ReadLog reads from a Byte Cursor to the end of the log.
//
// The cursor may land in the middle of a UTF-8 character or a line; the returned
// Text always starts at a line boundary, and NextStart always points at one. That
// is why the reader peeks at byte start-1: if it is a newline, start is already a
// boundary and nothing is dropped.
func (c *Client) ReadLog(ctx context.Context, fullName string, ref BuildRef, start int64) (*LogChunk, error) {
	if start < 0 {
		return nil, NewError(CodeInvalidParameter, "cursor must not be negative", "")
	}
	fetchStart := start
	if start > 0 {
		fetchStart = start - 1
	}
	path, err := progressivePath(fullName, ref, fetchStart)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(ctx, http.MethodGet, path, nil, "")
	if err != nil {
		return nil, err
	}
	lh := parseLogHeaders(resp.Header)
	body := resp.Body

	off := 0
	if start > 0 {
		if len(body) == 0 {
			return &LogChunk{NextStart: start, MoreData: lh.more, TotalSize: lh.total}, nil
		}
		if body[0] == '\n' {
			off = 1 // start was already a line boundary
		} else if idx := bytes.IndexByte(body, '\n'); idx >= 0 {
			off = idx + 1 // skip the partial line left over from an arbitrary cursor
		} else {
			// No boundary yet. If the build is done this fragment is the final
			// line and must be returned; otherwise wait for more data.
			if lh.more {
				return &LogChunk{NextStart: start, MoreData: true, TotalSize: lh.total}, nil
			}
			off = 0
		}
	}
	content := body[off:]
	absStart := fetchStart + int64(off)

	if lh.more {
		last := bytes.LastIndexByte(content, '\n')
		if last < 0 {
			// Everything available is one unterminated line; hold it back.
			return &LogChunk{NextStart: absStart, MoreData: true, TotalSize: lh.total}, nil
		}
		return &LogChunk{
			Text:      string(content[:last+1]),
			NextStart: absStart + int64(last) + 1,
			MoreData:  true,
			TotalSize: lh.total,
		}, nil
	}
	return &LogChunk{
		Text:      string(content),
		NextStart: lh.total,
		MoreData:  false,
		TotalSize: lh.total,
	}, nil
}

// TailLog returns the last lines of a log plus the honest totals behind them.
// It reads only the tail window, so cost does not grow with log size.
func (c *Client) TailLog(ctx context.Context, fullName string, ref BuildRef, lines int) (string, int64, bool, error) {
	total, _, err := c.LogStat(ctx, fullName, ref)
	if err != nil {
		return "", 0, false, err
	}
	start := total - tailWindow
	if start < 0 {
		start = 0
	}
	chunk, err := c.ReadLog(ctx, fullName, ref, start)
	if err != nil {
		return "", 0, false, err
	}
	text := chunk.Text
	if lines > 0 {
		text = lastLines(text, lines)
	}
	return text, chunk.TotalSize, chunk.MoreData, nil
}

// lastLines keeps at most n trailing lines, preserving a trailing newline when
// the input had one.
func lastLines(s string, n int) string {
	if n <= 0 || s == "" {
		return s
	}
	trimmed := strings.TrimSuffix(s, "\n")
	if trimmed == "" {
		return s
	}
	parts := strings.Split(trimmed, "\n")
	if len(parts) <= n {
		return s
	}
	kept := strings.Join(parts[len(parts)-n:], "\n")
	if strings.HasSuffix(s, "\n") {
		kept += "\n"
	}
	return kept
}

// CountLines counts newline-terminated lines. For a partial final line it counts
// one more, because that fragment is a line the reader will see.
func CountLines(s string) int {
	if s == "" {
		return 0
	}
	n := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}

// SpillLog copies the whole log to a file so that a truncated response loses
// nothing. It refuses logs past spillMax rather than filling the disk.
func (c *Client) SpillLog(ctx context.Context, fullName string, ref BuildRef, dir string) (string, error) {
	total, _, err := c.LogStat(ctx, fullName, ref)
	if err != nil {
		return "", err
	}
	if total > spillMax {
		return "", NewError(CodeInvalidParameter,
			fmt.Sprintf("log is %d bytes, above the %d byte spill limit", total, spillMax),
			"read it in chunks with the cursor instead")
	}
	chunk, err := c.ReadLog(ctx, fullName, ref, 0)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", NewError(CodeInternal, "could not create spill directory", err.Error())
	}
	safe := strings.NewReplacer("/", "_", "\\", "_", ":", "_").Replace(strings.Trim(fullName, "/"))
	name := fmt.Sprintf("%s-%s-%s.log", safe, ref, time.Now().Format("20060102-150405"))
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(chunk.Text), 0o600); err != nil {
		return "", NewError(CodeInternal, "could not write spill file", err.Error())
	}
	return path, nil
}

// GetPath is the Escape Hatch's transport: an arbitrary GET against the
// controller. It performs no path policy of its own; the tool layer is
// responsible for validating that the path is a same-origin absolute path.
func (c *Client) GetPath(ctx context.Context, path string) (*Response, error) {
	return c.do(ctx, http.MethodGet, path, nil, "")
}
