package jenkins

import "context"

// Node is one executor host: the controller itself or an agent.
type Node struct {
	DisplayName        string   `json:"display_name"`
	Offline            bool     `json:"offline"`
	TemporarilyOffline bool     `json:"temporarily_offline"`
	Idle               bool     `json:"idle"`
	NumExecutors       int      `json:"num_executors"`
	BusyExecutors      int      `json:"busy_executors"`
	Labels             []string `json:"labels"`
}

// Cluster is the executor picture an agent needs in order to explain "why is my
// build still queued".
type Cluster struct {
	TotalExecutors int    `json:"total_executors"`
	BusyExecutors  int    `json:"busy_executors"`
	IdleExecutors  int    `json:"idle_executors"`
	Nodes          []Node `json:"nodes"`
}

// ListNodes reports every executor host and how busy the cluster is.
//
// Per-node busy counts are derived from the executor array rather than from
// loadStatistics: on 2.492.3, loadStatistics.busyExecutors serialises as an empty
// object, not a number, so decoding it as an int fails outright.
func (c *Client) ListNodes(ctx context.Context) (*Cluster, error) {
	tree := "busyExecutors,totalExecutors," +
		"computer%5BdisplayName,offline,temporarilyOffline,numExecutors,idle," +
		"assignedLabels%5Bname%5D,executors%5Bidle,currentExecutable%5Bnumber%5D%5D%5D"
	var raw struct {
		BusyExecutors  int `json:"busyExecutors"`
		TotalExecutors int `json:"totalExecutors"`
		Computer       []struct {
			DisplayName        string `json:"displayName"`
			Offline            bool   `json:"offline"`
			TemporarilyOffline bool   `json:"temporarilyOffline"`
			NumExecutors       int    `json:"numExecutors"`
			Idle               bool   `json:"idle"`
			AssignedLabels     []struct {
				Name string `json:"name"`
			} `json:"assignedLabels"`
			Executors []struct {
				Idle              bool `json:"idle"`
				CurrentExecutable *struct {
					Number int64 `json:"number"`
				} `json:"currentExecutable"`
			} `json:"executors"`
		} `json:"computer"`
	}
	if err := c.getJSON(ctx, "/computer/api/json?tree="+tree, &raw); err != nil {
		return nil, err
	}
	out := &Cluster{
		TotalExecutors: raw.TotalExecutors,
		BusyExecutors:  raw.BusyExecutors,
		IdleExecutors:  raw.TotalExecutors - raw.BusyExecutors,
		Nodes:          make([]Node, 0, len(raw.Computer)),
	}
	for _, n := range raw.Computer {
		name := n.DisplayName
		if name == "" {
			name = "built-in"
		}
		labels := make([]string, 0, len(n.AssignedLabels))
		for _, l := range n.AssignedLabels {
			labels = append(labels, l.Name)
		}
		busy := 0
		for _, e := range n.Executors {
			if e.CurrentExecutable != nil {
				busy++
			}
		}
		out.Nodes = append(out.Nodes, Node{
			DisplayName:        name,
			Offline:            n.Offline,
			TemporarilyOffline: n.TemporarilyOffline,
			Idle:               n.Idle,
			NumExecutors:       n.NumExecutors,
			BusyExecutors:      busy,
			Labels:             labels,
		})
	}
	return out, nil
}
