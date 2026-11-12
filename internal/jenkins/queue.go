package jenkins

import (
	"context"
	"fmt"
	"net/url"
	"time"
)

// QueueItem is the controller's placeholder for a requested build.
type QueueItem struct {
	ID           int64         `json:"id"`
	Why          string        `json:"why,omitempty"`
	Blocked      bool          `json:"blocked"`
	Buildable    bool          `json:"buildable"`
	Stuck        bool          `json:"stuck"`
	Cancelled    bool          `json:"cancelled"`
	TaskName     string        `json:"task_name,omitempty"`
	TaskURL      string        `json:"task_url,omitempty"`
	InQueueSince int64         `json:"in_queue_since,omitempty"`
	Executable   *BuildSummary `json:"executable,omitempty"`
}

type queueItemJSON struct {
	ID           int64  `json:"id"`
	Why          string `json:"why"`
	Blocked      bool   `json:"blocked"`
	Buildable    bool   `json:"buildable"`
	Stuck        bool   `json:"stuck"`
	Cancelled    bool   `json:"cancelled"`
	InQueueSince int64  `json:"inQueueSince"`
	Task         struct {
		Name string `json:"name"`
		URL  string `json:"url"`
	} `json:"task"`
	Executable *struct {
		Number int64  `json:"number"`
		URL    string `json:"url"`
	} `json:"executable"`
}

// ListQueue lists everything currently waiting for an executor.
func (c *Client) ListQueue(ctx context.Context) ([]QueueItem, error) {
	var raw struct {
		Items []queueItemJSON `json:"items"`
	}
	tree := "items%5Bid,why,blocked,buildable,stuck,cancelled,inQueueSince,task%5Bname,url%5D,executable%5Bnumber,url%5D%5D"
	if err := c.getJSON(ctx, "/queue/api/json?tree="+tree, &raw); err != nil {
		return nil, err
	}
	out := make([]QueueItem, 0, len(raw.Items))
	for _, it := range raw.Items {
		out = append(out, toQueueItem(it))
	}
	return out, nil
}

func toQueueItem(it queueItemJSON) QueueItem {
	q := QueueItem{
		ID: it.ID, Why: it.Why, Blocked: it.Blocked, Buildable: it.Buildable,
		Stuck: it.Stuck, Cancelled: it.Cancelled, TaskName: it.Task.Name,
		TaskURL: it.Task.URL, InQueueSince: it.InQueueSince,
	}
	if it.Executable != nil {
		q.Executable = &BuildSummary{Number: it.Executable.Number, URL: it.Executable.URL}
	}
	return q
}

// GetQueueItem reads one queue item. A cancelled item disappears from the queue,
// which surfaces as not_found; callers treat that as "no longer waiting".
func (c *Client) GetQueueItem(ctx context.Context, id int64) (*QueueItem, error) {
	var raw queueItemJSON
	tree := "id,why,blocked,buildable,stuck,cancelled,inQueueSince,task%5Bname,url%5D,executable%5Bnumber,url%5D"
	if err := c.getJSON(ctx, fmt.Sprintf("/queue/item/%d/api/json?tree=%s", id, tree), &raw); err != nil {
		return nil, err
	}
	q := toQueueItem(raw)
	return &q, nil
}

// CancelQueueItem removes a waiting item. It is the only way to stop a build that
// has no build number yet.
func (c *Client) CancelQueueItem(ctx context.Context, id int64) error {
	if id <= 0 {
		return NewError(CodeInvalidParameter, "queue item id must be positive", "")
	}
	_, err := c.postForm(ctx, fmt.Sprintf("/queue/cancelItem?id=%d", id), url.Values{})
	return err
}

// TriggerResult is what a caller learns from asking for a build.
type TriggerResult struct {
	QueueID int64  `json:"queue_id"`
	Status  string `json:"status"` // queued | started
	// Why explains a queued outcome, e.g. "Waiting for next available executor".
	Why         string `json:"why,omitempty"`
	BuildNumber int64  `json:"build_number,omitempty"`
	BuildURL    string `json:"build_url,omitempty"`
}

// WaitForBuild polls a queue item until it turns into a build or the deadline
// passes. A queued result is a real answer, not an error: on a controller with no
// idle executor that is exactly what happened.
func (c *Client) WaitForBuild(ctx context.Context, queueID int64, wait time.Duration) (*TriggerResult, error) {
	res := &TriggerResult{QueueID: queueID, Status: "queued"}
	if wait <= 0 {
		if q, err := c.GetQueueItem(ctx, queueID); err == nil {
			res.Why = q.Why
			if q.Executable != nil {
				res.Status = "started"
				res.BuildNumber = q.Executable.Number
				res.BuildURL = q.Executable.URL
			}
		}
		return res, nil
	}
	deadline := time.Now().Add(wait)
	// Poll fast at first: most triggers start within a second or two.
	interval := 250 * time.Millisecond
	for {
		q, err := c.GetQueueItem(ctx, queueID)
		if err == nil {
			res.Why = q.Why
			if q.Executable != nil {
				res.Status = "started"
				res.BuildNumber = q.Executable.Number
				res.BuildURL = q.Executable.URL
				return res, nil
			}
			if q.Cancelled {
				return nil, NewError(CodeNotFound, "the queued build was cancelled before it started", res.Why)
			}
		} else if je := AsError(err); je.Code != CodeNotFound {
			// A transient read failure should not lose the trigger: keep the
			// queue id and let the caller poll.
			return res, nil
		}
		if time.Now().After(deadline) {
			return res, nil
		}
		select {
		case <-ctx.Done():
			return res, ctx.Err()
		case <-time.After(interval):
		}
		if interval < 2*time.Second {
			interval += 250 * time.Millisecond
		}
	}
}
