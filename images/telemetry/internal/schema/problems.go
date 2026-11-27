package schema

import "slices"

// Problem thresholds (docs/telemetry.md "Problem thresholds").
const (
	CPUThreshold      = 85.0
	MemoryThreshold   = 90.0
	DiskThreshold     = 90.0
	RestartGrowthLoop = 3
)

// Problem kinds.
var ProblemKinds = []string{"update_failed", "rollback", "crash_loop", "unhealthy", "cpu", "memory", "disk"}

// Problems are the kinds one report shows, given the install's previous
// report (nil if none) for restart growth. Services lists the ones that
// crash-loop or are unhealthy.
type Problems struct {
	Kinds    []string `json:"kinds"`
	Errors   []string `json:"errors,omitempty"`
	Looping  []string `json:"looping,omitempty"`
	Sick     []string `json:"unhealthy,omitempty"`
	Failures int      `json:"failures,omitempty"`
}

func (p *Problems) add(k string) {
	if !slices.Contains(p.Kinds, k) {
		p.Kinds = append(p.Kinds, k)
	}
}

// Of works out the problems of r.
func Of(r, prev *Report) Problems {
	var p Problems
	if r.Health.Updates.Failures > 0 {
		p.add("update_failed")
		p.Failures = r.Health.Updates.Failures
		p.Errors = append(p.Errors, r.Health.Updates.Errors...)
	}
	if r.Health.Updates.Rollbacks > 0 {
		p.add("rollback")
	}
	for name, s := range r.Health.Services {
		grew := false
		if prev != nil {
			if ps, ok := prev.Health.Services[name]; ok && s.Restarts-ps.Restarts >= RestartGrowthLoop {
				grew = true
			}
		}
		if s.State == "restarting" || grew {
			p.add("crash_loop")
			p.Looping = append(p.Looping, name)
		}
		if s.State == "unhealthy" {
			p.add("unhealthy")
			p.Sick = append(p.Sick, name)
		}
	}
	if !r.Health.Healthy {
		p.add("unhealthy")
	}
	if h := r.Health.Host; h != nil {
		if h.CPUP95 != nil && *h.CPUP95 > CPUThreshold {
			p.add("cpu")
		}
		if h.MemP95 != nil && *h.MemP95 > MemoryThreshold {
			p.add("memory")
		}
		if h.DiskUsed != nil && *h.DiskUsed > DiskThreshold {
			p.add("disk")
		}
	}
	slices.Sort(p.Looping)
	slices.Sort(p.Sick)
	slices.SortFunc(p.Kinds, func(a, b string) int {
		return slices.Index(ProblemKinds, a) - slices.Index(ProblemKinds, b)
	})
	return p
}

// Merge adds q's problems to p (several reports of one install).
func (p *Problems) Merge(q Problems) {
	for _, k := range q.Kinds {
		p.add(k)
	}
	for _, e := range q.Errors {
		if !slices.Contains(p.Errors, e) {
			p.Errors = append(p.Errors, e)
		}
	}
	for _, s := range q.Looping {
		if !slices.Contains(p.Looping, s) {
			p.Looping = append(p.Looping, s)
		}
	}
	for _, s := range q.Sick {
		if !slices.Contains(p.Sick, s) {
			p.Sick = append(p.Sick, s)
		}
	}
	p.Failures += q.Failures
	slices.Sort(p.Errors)
	slices.Sort(p.Looping)
	slices.Sort(p.Sick)
	slices.SortFunc(p.Kinds, func(a, b string) int {
		return slices.Index(ProblemKinds, a) - slices.Index(ProblemKinds, b)
	})
}
