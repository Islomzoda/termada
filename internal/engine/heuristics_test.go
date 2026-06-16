package engine

import (
	"strings"
	"testing"
	"time"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		argv []string
		want string
	}{
		{[]string{"go", "build", "./..."}, "build"},
		{[]string{"make"}, "build"},
		{[]string{"go", "test", "./..."}, "test"},
		{[]string{"pytest", "-q"}, "test"},
		{[]string{"npm", "install"}, "install"},
		{[]string{"sudo", "apt-get", "install", "vim"}, "install"},
		{[]string{"env", "PGPASSWORD=x", "psql", "-c", "select 1"}, "db"},
		{[]string{"curl", "https://x"}, "network"},
		{[]string{"echo", "hi"}, "default"},
		{[]string{"npm", "run", "dev"}, "default"},
	}
	for _, c := range cases {
		if got := classify(c.argv); got != c.want {
			t.Errorf("classify(%v) = %q, want %q", c.argv, got, c.want)
		}
	}
}

func TestIsDaemon(t *testing.T) {
	yes := [][]string{
		{"npm", "run", "dev"},
		{"python", "-m", "http.server"},
		{"docker", "compose", "up", "-d"},
		{"tail", "-f", "/var/log/x"},
		{"sudo", "-E", "rails", "server"},
	}
	no := [][]string{
		{"echo", "hi"},
		{"go", "build"},
		{"ls", "-la"},
	}
	for _, a := range yes {
		if !isDaemon(a) {
			t.Errorf("isDaemon(%v) = false, want true", a)
		}
	}
	for _, a := range no {
		if isDaemon(a) {
			t.Errorf("isDaemon(%v) = true, want false", a)
		}
	}
}

func TestClassTimeout(t *testing.T) {
	m := newTestManager(t)
	m.SetTimeoutClasses(map[string]int{"test": 1234, "default": 30000})
	if got := m.classTimeout([]string{"go", "test"}); got != 1234 {
		t.Fatalf("class timeout test = %d, want 1234", got)
	}
	if got := m.classTimeout([]string{"echo"}); got != 30000 {
		t.Fatalf("class timeout default = %d, want 30000", got)
	}
}

func TestAutoBackgroundsDaemon(t *testing.T) {
	m := newTestManager(t)
	start := time.Now()
	res, err := m.Run("agent", "", []string{"tail", "-f", "/dev/null"}, ModeAuto, 0)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if time.Since(start) > 4*time.Second {
		t.Fatalf("daemon should background quickly, took %v", time.Since(start))
	}
	if res.Status != StatusBackgrounded {
		t.Fatalf("status = %s, want backgrounded", res.Status)
	}
	// cleanup: kill the still-running tail
	for i := 0; i < 40 && m.Kill(res.JobID) != nil; i++ {
		time.Sleep(50 * time.Millisecond)
	}
}

func TestAutoWaitsForQuickCommand(t *testing.T) {
	m := newTestManager(t)
	res, err := m.Run("agent", "", []string{"echo", "quick-result"}, ModeAuto, 0)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != StatusExited || !strings.Contains(res.Stdout, "quick-result") {
		t.Fatalf("quick command: status=%s out=%q", res.Status, res.Stdout)
	}
}
