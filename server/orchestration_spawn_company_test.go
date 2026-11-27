package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// spawn_task with company_ids links those companies at creation: the live task
// carries them in its lifecycle, they persist in task_scope, and the
// company-filtered task list returns the task. An unknown company id is a clean
// tool error that creates nothing; a spawn without company_ids links nothing
// (the pre-fix behavior: invisible in every company-filtered view).
func TestSpawnTaskCompanyIDs(t *testing.T) {
	skipWithoutDSN(t)
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := New(ctx, m, t.TempDir(), t.TempDir(), t.TempDir())

	stamp := time.Now().UnixNano()
	companyA, _, err := m.pg.Companies().UpsertCompany(fmt.Sprintf("spawn-co-a-%d", stamp), "")
	if err != nil {
		t.Fatal(err)
	}
	companyB, _, err := m.pg.Companies().UpsertCompany(fmt.Sprintf("spawn-co-b-%d", stamp), "")
	if err != nil {
		t.Fatal(err)
	}
	companyC, _, err := m.pg.Companies().UpsertCompany(fmt.Sprintf("spawn-co-c-%d", stamp), "")
	if err != nil {
		t.Fatal(err)
	}

	cleanupTask := func(id string) {
		t.Helper()
		t.Cleanup(func() {
			n, _ := strconv.ParseInt(id, 10, 64)
			_ = m.pg.DeleteTask(n)
		})
	}

	spawn := func(input map[string]any) string {
		t.Helper()
		in, _ := json.Marshal(input)
		res, err := s.toolSpawnTask().Call(context.Background(), in, nil)
		if err != nil {
			t.Fatal(err)
		}
		return res.Flatten()
	}

	t.Run("linked companies land on the task and in the filtered list", func(t *testing.T) {
		// one id as a JSON number, the other as a numeric string: the tool must
		// tolerate both, like the other id-bearing orchestration args.
		out := spawn(map[string]any{
			"description": "spawn with companies", "goal": "link company scopes",
			"company_ids": []any{companyA, fmt.Sprintf("%d", companyB)},
		})
		childID, found := strings.CutPrefix(out, "task created: ")
		if !found {
			t.Fatalf("spawn_task result: %q", out)
		}
		cleanupTask(childID)

		child, ok := m.Task(childID)
		if !ok {
			t.Fatal("spawned task not found in manager")
		}
		got := child.lifecycleSnapshot().CompanyIDs
		if len(got) != 2 || !slices.Contains(got, companyA) || !slices.Contains(got, companyB) {
			t.Fatalf("live task CompanyIDs = %+v, want both [%d %d]", got, companyA, companyB)
		}
		persisted, err := m.pg.GetTask(mustTaskID(t, childID))
		if err != nil {
			t.Fatal(err)
		}
		if persisted == nil || len(persisted.CompanyIDs) != 2 {
			t.Fatalf("persisted CompanyIDs = %+v", persisted)
		}

		// The company filter of the tasks list must return the task under either
		// linked company, and not under an unlinked one.
		listContains := func(companyID int64, want bool) {
			t.Helper()
			req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/tasks?company=%d", companyID), nil)
			rec := httptest.NewRecorder()
			s.listTasks(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("list tasks status=%d body=%s", rec.Code, rec.Body.String())
			}
			var payload struct {
				Tasks []TaskDTO `json:"tasks"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			foundTask := false
			for _, dto := range payload.Tasks {
				if dto.ID == childID {
					foundTask = true
				}
			}
			if foundTask != want {
				t.Fatalf("company=%d list contains task %s: %v, want %v", companyID, childID, foundTask, want)
			}
		}
		listContains(companyA, true)
		listContains(companyB, true)
		listContains(companyC, false)
	})

	t.Run("unknown company id is a clean error and creates nothing", func(t *testing.T) {
		before := len(m.List())
		out := spawn(map[string]any{
			"description": "spawn with unknown company", "goal": "must fail",
			"company_ids": []any{999999999},
		})
		if strings.HasPrefix(out, "task created: ") {
			t.Fatalf("spawn with unknown company succeeded: %q", out)
		}
		if !strings.Contains(out, "company") {
			t.Fatalf("error mentions no company: %q", out)
		}
		if len(m.List()) != before {
			t.Fatalf("failed spawn created a task: before=%d after=%d", before, len(m.List()))
		}
	})

	t.Run("non-numeric company id is rejected", func(t *testing.T) {
		out := spawn(map[string]any{
			"description": "spawn with junk company id", "goal": "must fail",
			"company_ids": []any{"not-a-number"},
		})
		if strings.HasPrefix(out, "task created: ") {
			t.Fatalf("spawn with junk company id succeeded: %q", out)
		}
	})

	t.Run("spawn without company_ids links nothing", func(t *testing.T) {
		out := spawn(map[string]any{"description": "spawn bare", "goal": "no companies"})
		childID, found := strings.CutPrefix(out, "task created: ")
		if !found {
			t.Fatalf("spawn_task result: %q", out)
		}
		cleanupTask(childID)
		child, ok := m.Task(childID)
		if !ok {
			t.Fatal("spawned task not found in manager")
		}
		if got := child.lifecycleSnapshot().CompanyIDs; len(got) != 0 {
			t.Fatalf("bare spawn CompanyIDs = %+v, want empty", got)
		}
	})
}
