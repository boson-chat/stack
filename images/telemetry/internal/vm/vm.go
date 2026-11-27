// Package vm writes daily points to VictoriaMetrics (/api/v1/import, JSON
// lines) and reads them back (/api/v1/query_range) for the panel's charts.
package vm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"time"
)

// Sample is one point: a metric name, its labels, a value at a time.
type Sample struct {
	Name   string
	Labels map[string]string
	Value  float64
	Time   time.Time
}

// Client talks to one VictoriaMetrics.
type Client struct {
	URL  string
	HTTP *http.Client
}

type line struct {
	Metric     map[string]string `json:"metric"`
	Values     []float64         `json:"values"`
	Timestamps []int64           `json:"timestamps"`
}

// Import writes samples.
func (c *Client) Import(ctx context.Context, samples []Sample) error {
	if len(samples) == 0 {
		return nil
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, s := range samples {
		m := map[string]string{"__name__": s.Name}
		for k, v := range s.Labels {
			m[k] = v
		}
		if err := enc.Encode(line{Metric: m, Values: []float64{s.Value}, Timestamps: []int64{s.Time.UnixMilli()}}); err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL+"/api/v1/import", &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("import: %s: %s", resp.Status, b)
	}
	return nil
}

// Series is one result series of a range query.
type Series struct {
	Labels map[string]string `json:"labels"`
	Points []Point           `json:"points"`
}

// Point is a day and a value.
type Point struct {
	T int64   `json:"t"` // unix seconds
	V float64 `json:"v"`
}

// QueryRange runs query from start to end in steps of a day. Each point is
// the last sample in the day before it, so daily points written at 00:00
// UTC land on their own day.
func (c *Client) QueryRange(ctx context.Context, query string, start, end time.Time) ([]Series, error) {
	q := url.Values{}
	q.Set("query", query)
	q.Set("start", strconv.FormatInt(start.Unix(), 10))
	q.Set("end", strconv.FormatInt(end.Unix(), 10))
	q.Set("step", "1d")
	q.Set("nocache", "1")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL+"/api/v1/query_range?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("query_range: %s: %s", resp.Status, b)
	}
	var body struct {
		Data struct {
			Result []struct {
				Metric map[string]string `json:"metric"`
				Values [][2]any          `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	out := make([]Series, 0, len(body.Data.Result))
	for _, r := range body.Data.Result {
		s := Series{Labels: r.Metric}
		for _, v := range r.Values {
			ts, _ := v[0].(float64)
			str, _ := v[1].(string)
			f, err := strconv.ParseFloat(str, 64)
			if err != nil {
				continue
			}
			s.Points = append(s.Points, Point{T: int64(ts), V: f})
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return fmt.Sprint(out[i].Labels) < fmt.Sprint(out[j].Labels) })
	return out, nil
}

// Ready checks /health.
func (c *Client) Ready(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL+"/health", nil)
	if err != nil {
		return err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health: %s", resp.Status)
	}
	return nil
}
