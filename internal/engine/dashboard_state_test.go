package engine

import (
	"fmt"
	"testing"
	"time"
)

func TestListJobsLimitedReturnsNewestPageAndTotal(t *testing.T) {
	m := NewManager(DefaultConfig())
	base := time.Now().UnixMilli()
	m.mu.Lock()
	for i := 0; i < 12; i++ {
		m.recovered = append(m.recovered, Info{
			JobID:         fmt.Sprintf("job_%02d", i),
			Owner:         "agent",
			Workspace:     "termada",
			Status:        StatusExited,
			CreatedUnixMS: base + int64(i),
		})
	}
	m.mu.Unlock()

	jobs, total := m.ListJobsLimited("agent", "recent", 3)
	if total != 12 || len(jobs) != 3 {
		t.Fatalf("limited jobs = %d total=%d, want 3/12", len(jobs), total)
	}
	if jobs[0].JobID != "job_11" || jobs[2].JobID != "job_09" {
		t.Fatalf("limited order = %s..%s, want job_11..job_09", jobs[0].JobID, jobs[2].JobID)
	}
	if jobs[0].Workspace != "termada" {
		t.Fatalf("workspace = %q, want termada", jobs[0].Workspace)
	}
}

func BenchmarkListJobsLimited500(b *testing.B) {
	m := NewManager(DefaultConfig())
	base := time.Now().UnixMilli()
	for i := 0; i < 500; i++ {
		m.recovered = append(m.recovered, Info{JobID: fmt.Sprintf("job_%03d", i), Status: StatusExited, CreatedUnixMS: base + int64(i)})
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		jobs, total := m.ListJobsLimited("", "recent", 100)
		if len(jobs) != 100 || total != 500 {
			b.Fatal("unexpected page")
		}
	}
}
