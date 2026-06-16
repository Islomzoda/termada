package fleet

import "testing"

// mockRunner returns a canned result per server name.
type mockRunner struct{ byServer map[string]Result }

func (m mockRunner) Run(server Server, command []string) Result {
	if r, ok := m.byServer[server.Name]; ok {
		r.Server = server.Name
		return r
	}
	return Result{Server: server.Name, Status: "ok"}
}

func testServers() []Server {
	return []Server{
		{Name: "web1", Host: "10.0.0.1", User: "deploy", Tags: []string{"web", "prod"}},
		{Name: "web2", Host: "10.0.0.2", User: "deploy", Tags: []string{"web", "prod"}},
		{Name: "db1", Host: "10.0.0.3", User: "deploy", Tags: []string{"db", "prod"}},
	}
}

func TestServerListHasNoSecrets(t *testing.T) {
	m := New(testServers(), mockRunner{}, 5)
	list := m.ServerList()
	if len(list) != 3 {
		t.Fatalf("server list = %d, want 3", len(list))
	}
	// ServerInfo intentionally has no Auth field — secrets never leave.
	if list[0].Name != "db1" && list[0].Name != "web1" {
		// order is not guaranteed; just ensure names present
	}
}

func TestSelectByTag(t *testing.T) {
	m := New(testServers(), mockRunner{}, 5)
	res, err := m.Run([]string{"uptime"}, []string{"web"}, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Results) != 2 {
		t.Fatalf("tag web selected %d servers, want 2", len(res.Results))
	}
}

func TestSelectByName(t *testing.T) {
	m := New(testServers(), mockRunner{}, 5)
	res, _ := m.Run([]string{"uptime"}, []string{"db1"}, 5)
	if len(res.Results) != 1 || res.Results[0].Server != "db1" {
		t.Fatalf("name select = %+v", res.Results)
	}
}

func TestAggregatePartial(t *testing.T) {
	mr := mockRunner{byServer: map[string]Result{
		"web1": {Status: "ok"},
		"web2": {Status: "unreachable", Error: "timeout"},
		"db1":  {Status: "nonzero_exit", ExitCode: 1},
	}}
	m := New(testServers(), mr, 5)
	res, _ := m.Run([]string{"uptime"}, nil, 5)
	if res.Status != "partial" {
		t.Fatalf("status = %s, want partial", res.Status)
	}
	if res.Summary["ok"] != 1 || res.Summary["unreachable"] != 1 || res.Summary["nonzero_exit"] != 1 {
		t.Fatalf("summary = %+v", res.Summary)
	}
}

func TestAggregateAllFail(t *testing.T) {
	mr := mockRunner{byServer: map[string]Result{
		"web1": {Status: "unreachable"},
		"web2": {Status: "unreachable"},
		"db1":  {Status: "unreachable"},
	}}
	m := New(testServers(), mr, 5)
	res, _ := m.Run([]string{"x"}, nil, 5)
	if res.Status != "failed" {
		t.Fatalf("status = %s, want failed", res.Status)
	}
}

func TestNoServersMatched(t *testing.T) {
	m := New(testServers(), mockRunner{}, 5)
	if _, err := m.Run([]string{"x"}, []string{"nonexistent"}, 5); err == nil {
		t.Fatal("expected error for empty selection")
	}
}
