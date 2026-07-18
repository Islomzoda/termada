package mission

import (
	"fmt"
	"strings"
	"time"

	"github.com/termada/termada/internal/audit"
)

// Report is the agent-facing exported evidence artifact.
type Report struct {
	MissionID string `json:"mission_id"`
	Format    string `json:"format"`
	SHA256    string `json:"sha256"`
	Content   string `json:"report"`
}

// ReportMarkdown exports a deterministic evidence report from stored runtime
// events. It contains no terminal output and makes agent-supplied notes explicit.
func ReportMarkdown(m *Mission) string {
	return ReportMarkdownWithAudit(m, nil)
}

// ReportMarkdownWithAudit adds exact hash-chain anchors for the mission's
// session attempts. The caller supplies a verified/bounded audit tail.
func ReportMarkdownWithAudit(m *Mission, records []audit.Record) string {
	if m == nil {
		return ""
	}
	var out strings.Builder
	fmt.Fprintf(&out, "# Evidence Report: %s\n\n", cleanMarkdown(m.Title))
	fmt.Fprintf(&out, "- Mission: `%s`\n", m.ID)
	fmt.Fprintf(&out, "- Status: **%s**\n", m.Status)
	fmt.Fprintf(&out, "- Target: `%s`\n", m.Target)
	fmt.Fprintf(&out, "- Current session: `%s`\n", m.SessionID)
	if len(m.SessionIDs) > 1 {
		fmt.Fprintf(&out, "- Session attempts: %d (`%s`)\n", len(m.SessionIDs), strings.Join(m.SessionIDs, "`, `"))
	}
	fmt.Fprintf(&out, "- Started: %s\n", formatTime(m.CreatedAt))
	if m.CompletedAt != nil {
		fmt.Fprintf(&out, "- Completed: %s\n", formatTime(*m.CompletedAt))
	}
	fmt.Fprintf(&out, "- Agent: `%s`\n\n", m.Owner)

	out.WriteString("## Goal\n\n")
	out.WriteString(cleanMarkdown(m.Goal) + "\n\n")
	if m.Summary != "" {
		out.WriteString("## Outcome\n\n")
		out.WriteString(cleanMarkdown(m.Summary) + "\n\n")
	}

	out.WriteString("## Plan And Verified Steps\n\n")
	for _, step := range m.Steps {
		mark := " "
		if step.Status == StepPassed || step.Status == StepSkipped {
			mark = "x"
		}
		fmt.Fprintf(&out, "- [%s] **%s**: %s", mark, step.ID, cleanMarkdown(step.Title))
		if step.JobID != "" {
			fmt.Fprintf(&out, " (`%s`)", step.JobID)
		}
		fmt.Fprintf(&out, " - %s", step.Status)
		if step.Note != "" {
			fmt.Fprintf(&out, ". Agent note: %s", cleanMarkdown(step.Note))
		}
		out.WriteString("\n")
	}

	out.WriteString("\n## Runtime Evidence\n\n")
	out.WriteString("| Time (UTC) | Event | Command / detail | Result |\n")
	out.WriteString("|---|---|---|---|\n")
	for _, event := range m.Events {
		if !reportable(event.Type) {
			continue
		}
		result := event.Status
		if event.ExitCode != nil {
			if result != "" {
				result += ", "
			}
			result += fmt.Sprintf("exit %d", *event.ExitCode)
		}
		if event.Approved != nil {
			if *event.Approved {
				result = "approved"
			} else {
				result = "denied"
			}
			if event.By != "" {
				result += " by " + event.By
			}
		}
		fmt.Fprintf(&out, "| %s | `%s` | %s | %s |\n", event.Time.UTC().Format("15:04:05"), event.Type, tableCell(event.Message), tableCell(result))
	}

	anchors := auditAnchors(m, records)
	if len(anchors) > 0 {
		out.WriteString("\n## Audit Chain Anchors\n\n")
		out.WriteString("| Seq | Event | Job | Record hash |\n")
		out.WriteString("|---:|---|---|---|\n")
		for _, record := range anchors {
			fmt.Fprintf(&out, "| %d | `%s` | `%s` | `%s` |\n", record.Seq, record.Type, record.JobID, record.Hash)
		}
	}

	out.WriteString("\n## Integrity And Limits\n\n")
	out.WriteString("Mission evidence is correlated from Termada runtime events and persisted locally with mode `0600`. Steps marked `passed` reference a job from this mission session that Termada observed exiting with code 0. The report does not capture terminal output, and agent notes are not independently verified. Verify the separate tamper-evident audit chain with `termada audit verify`.\n")
	return out.String()
}

func auditAnchors(m *Mission, records []audit.Record) []audit.Record {
	if len(records) == 0 {
		return nil
	}
	sessions := map[string]bool{}
	for _, id := range m.SessionIDs {
		sessions[id] = true
	}
	if m.SessionID != "" {
		sessions[m.SessionID] = true
	}
	out := make([]audit.Record, 0)
	for _, record := range records {
		if record.Type == "mission.report_generated" {
			continue
		}
		missionID, _ := record.Data["mission_id"].(string)
		if sessions[record.SessionID] || missionID == m.ID {
			out = append(out, record)
		}
	}
	if len(out) > maxEvents {
		out = out[len(out)-maxEvents:]
	}
	return out
}

func reportable(eventType string) bool {
	return eventType == "job.started" || eventType == "job.finished" || eventType == "confirm.requested" || eventType == "confirm.resolved" || eventType == "policy.denied" || eventType == "session.reset" || eventType == "mission.step_updated" || eventType == "mission.completed" || eventType == "mission.resumed" || eventType == "mission.interrupted"
}

func cleanMarkdown(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "\r", ""), "\x00", "")
}

func tableCell(value string) string {
	value = cleanMarkdown(value)
	value = strings.ReplaceAll(value, "|", "\\|")
	value = strings.ReplaceAll(value, "\n", "<br>")
	return value
}

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339)
}
