package main

import (
	"encoding/json"
	"errors"
	"github.com/HyperPteLtd/openwrt-hyper/internal/router"
	"os"
	"strconv"
	"strings"
)

const taskFile = router.Root + "/private/tasks.json"

type journal struct {
	Schema int   `json:"schema_version"`
	Tasks  []job `json:"tasks"`
}

func (s *app) loadJobs() error {
	b, e := os.ReadFile(taskFile)
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return e
	}
	var saved journal
	if json.Unmarshal(b, &saved) != nil || saved.Schema != 1 || len(saved.Tasks) > 128 {
		return errors.New("unrecognized task journal; state retained")
	}
	for _, j := range saved.Tasks {
		if j.Stage == "running" {
			j.Stage = "interrupted"
			j.Error = "manager restarted before completion"
		}
		s.jobs[j.ID] = j
		s.order = append(s.order, j.ID)
	}
	return nil
}

// Job bodies can contain credentials. Only task IDs, operation names and
// outcomes are persisted. The caller can reread the corresponding resource.
func (s *app) saveJobs() error {
	saved := journal{Schema: 1, Tasks: make([]job, 0, len(s.order))}
	for _, id := range s.order {
		j := s.jobs[id]
		if j.Action == bootstrapAction {
			j.Result = publicBootstrapResult(j.Result)
		} else {
			j.Result = nil
		}
		saved.Tasks = append(saved.Tasks, j)
	}
	b, e := json.Marshal(saved)
	if e != nil {
		return e
	}
	return router.Atomic(taskFile, b, 0600)
}
func revisionTask(id uint64) map[string]any {
	return map[string]any{"task": "intent-" + strconv.FormatUint(id, 10), "revision": id}
}
func (s *app) task(id string) (any, error) {
	if number, ok := strings.CutPrefix(id, "intent-"); ok {
		rev, e := strconv.ParseUint(number, 10, 64)
		st := s.m.Status()
		if e != nil || rev == 0 || rev > st.Intent.Revision {
			return nil, errors.New("unknown intent")
		}
		stage := "replaced"
		problem := ""
		for _, r := range s.m.GroupState(st.Intent.Source) {
			if r.Revision == rev {
				stage = r.State
				problem = r.Error
				return job{ID: id, Stage: stage, Action: "select_node", Error: problem}, nil
			}
		}
		if rev == st.Intent.Revision {
			stage = st.Phase
			problem = st.Problem
			if rev == st.Applied {
				stage = "applied"
			}
		}
		return job{ID: id, Stage: stage, Action: "service", Error: problem}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	x, ok := s.jobs[id]
	if !ok {
		return nil, errors.New("unknown task")
	}
	return x, nil
}
