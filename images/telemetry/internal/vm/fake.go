package vm

import (
	"context"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Fake is an in-memory stand-in for tests and the dev server. It keeps
// imported samples and answers the panel's queries: a metric name, an
// optional {label="value"} filter, and an optional sum / sum by (label).
type Fake struct {
	mu      sync.Mutex
	Samples []Sample
	Fail    error
}

// Import stores samples.
func (f *Fake) Import(_ context.Context, s []Sample) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Fail != nil {
		return f.Fail
	}
	f.Samples = append(f.Samples, s...)
	return nil
}

var (
	nameRe   = regexp.MustCompile(`boson_[a-z_]+`)
	filterRe = regexp.MustCompile(`\{([a-z_]+)="([^"]*)"\}`)
	byRe     = regexp.MustCompile(`sum by \(([a-z_]+)\)`)
)

// QueryRange groups the stored daily samples by day.
func (f *Fake) QueryRange(_ context.Context, query string, start, end time.Time) ([]Series, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Fail != nil {
		return nil, f.Fail
	}
	name := nameRe.FindString(query)
	var fk, fv string
	if m := filterRe.FindStringSubmatch(query); m != nil {
		fk, fv = m[1], m[2]
	}
	by, sum := "", strings.Contains(query, "sum(")
	if m := byRe.FindStringSubmatch(query); m != nil {
		by = m[1]
	}
	groups := map[string]*Series{}
	acc := map[string]map[int64]float64{}
	for _, s := range f.Samples {
		if s.Name != name || s.Time.Before(start) || s.Time.After(end) {
			continue
		}
		if fk != "" && s.Labels[fk] != fv {
			continue
		}
		labels := map[string]string{}
		switch {
		case by != "":
			labels[by] = s.Labels[by]
		case sum:
		default:
			for k, v := range s.Labels {
				labels[k] = v
			}
		}
		keys := make([]string, 0, len(labels))
		for k, v := range labels {
			keys = append(keys, k+"="+v)
		}
		sort.Strings(keys)
		k := strings.Join(keys, ",")
		if groups[k] == nil {
			groups[k] = &Series{Labels: labels}
			acc[k] = map[int64]float64{}
		}
		acc[k][s.Time.Unix()] += s.Value
	}
	var out []Series
	for k, g := range groups {
		for t, v := range acc[k] {
			g.Points = append(g.Points, Point{T: t, V: v})
		}
		sort.Slice(g.Points, func(i, j int) bool { return g.Points[i].T < g.Points[j].T })
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool {
		return len(out[i].Labels) > 0 && len(out[j].Labels) > 0 && labelsKey(out[i].Labels) < labelsKey(out[j].Labels)
	})
	return out, nil
}

func labelsKey(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k, v := range m {
		keys = append(keys, k+"="+v)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}
