package dashboard

import (
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func TestHandlerServesStrictCSPWithoutInlineEventHandlers(t *testing.T) {
	h := Handler()
	request := func() (*http.Response, string) {
		t.Helper()
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
		res := rr.Result()
		body, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		return res, string(body)
	}

	res, body := request()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	csp := res.Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "script-src 'self'") {
		t.Fatalf("CSP does not restrict scripts to embedded assets: %q", csp)
	}
	scriptDirective := strings.SplitN(strings.SplitN(csp, "script-src ", 2)[1], ";", 2)[0]
	if strings.Contains(scriptDirective, "'unsafe-inline'") {
		t.Fatalf("script-src permits unsafe inline scripts: %q", scriptDirective)
	}
	if strings.Contains(body, "__ASSET_VERSION__") || !strings.Contains(body, `src="app.js?v=`) {
		t.Fatal("dashboard assets are not content-versioned")
	}
	markup := body
	inlineHandler := regexp.MustCompile(`(?i)\son[a-z]+\s*=`)
	if inlineHandler.MatchString(markup) {
		t.Fatalf("dashboard contains an inline event handler: %q", inlineHandler.FindString(markup))
	}
	source := body + dashboardAsset(t, "assets/app.js")
	if !strings.Contains(source, `replace(/[&<>"']/g`) || !strings.Contains(source, `'"':'&quot;'`) || !strings.Contains(source, `"'":'&#39;'`) {
		t.Fatal("HTML escaping does not cover both attribute quote characters")
	}
	if strings.Contains(source, "localStorage") || !strings.Contains(source, "sessionStorage") {
		t.Fatal("dashboard token must be scoped to the browser tab, not persisted in localStorage")
	}
	for _, required := range []string{
		`class="termnotice"`,
		`aria-live="assertive"`,
		`m.awaiting_input`,
		`m.prompt`,
		`m.gap`,
		`queueTermInput`,
		`inputQueue:Promise.resolve()`,
		`!info.inputFailed&&Object.prototype.hasOwnProperty.call`,
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("dashboard is missing interactive prompt support %q", required)
		}
	}

	_, secondBody := request()
	if secondBody != body {
		t.Fatal("content-versioned dashboard index changed between identical requests")
	}
}

func TestHandlerSecurityHeadersApplyToStaticAssets(t *testing.T) {
	rr := httptest.NewRecorder()
	Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/vendor/xterm.js", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if got := rr.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q", got)
	}
	if got := rr.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Fatalf("X-Frame-Options = %q", got)
	}
}

func TestHandlerCachesContentVersionedAssets(t *testing.T) {
	index := dashboardIndexBody(t)
	match := regexp.MustCompile(`app\.js\?v=([a-f0-9]+)`).FindStringSubmatch(index)
	if len(match) != 2 {
		t.Fatal("dashboard index is missing the app asset version")
	}
	rr := httptest.NewRecorder()
	Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/app.js?v="+match[1], nil))
	if rr.Code != http.StatusOK || !strings.Contains(rr.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("versioned asset status/cache = %d %q", rr.Code, rr.Header().Get("Cache-Control"))
	}
}

func TestDashboardAssetsStayWithinRuntimeBudgets(t *testing.T) {
	index := dashboardIndexBody(t)
	css := dashboardAsset(t, "assets/app.css")
	js := dashboardAsset(t, "assets/app.js")
	if len(index) > 12<<10 || len(css) > 28<<10 || len(js) > 88<<10 {
		t.Fatalf("dashboard asset budget exceeded: index=%d css=%d js=%d", len(index), len(css), len(js))
	}
	for _, required := range []string{
		`@media(max-width:780px)`,
		`.layout.sidebar-open aside`,
		`requestAnimationFrame(`,
		`/api/dashboard/state?limit=100`,
		`activeJobsCount>0?2000:45000`,
	} {
		if !strings.Contains(css+js, required) {
			t.Fatalf("dashboard budget primitive missing %q", required)
		}
	}
	if strings.Contains(js, "setInterval(") {
		t.Fatal("dashboard reintroduced unconditional polling")
	}
}

func TestDashboardContainsDialogTreeAndTerminalFallback(t *testing.T) {
	body := dashboardBody(t)
	for _, required := range []string{
		`function renderDialogTree(`,
		`function buildConversationGroups(`,
		`function conversationKey(`,
		`const UNKNOWN_TARGET='__unknown__',OPERATOR_OWNER='__operator__'`,
		`JSON.stringify([canonicalOwner(owner),target||UNKNOWN_TARGET,workspace||''])`,
		`function displayWorkspace(`,
		`function displayOwner(owner){return owner===OPERATOR_OWNER?tr('operator')`,
		`class="dialog-group"`,
		`class="conversation-route"`,
		`/api/dashboard/state?limit=100`,
		`id="execution-panel" aria-label="Conversation runs"`,
		`data-filter="active"`,
		`data-filter="failed"`,
		`role="region" aria-label="Request timeline"`,
		`aria-live="polite"`,
		`data-view="dialog" aria-pressed="true" data-i18n="output"`,
		`data-view="terminal" aria-pressed="false" data-i18n="terminal"`,
		`b.setAttribute('aria-pressed',String(selected))`,
		`function setTermView(`,
		`Object.keys(terms).forEach(openKey=>disposeTerm(openKey))`,
		`function jobFailed(`,
	} {
		if !strings.Contains(body, required) {
			t.Fatalf("dashboard is missing dialog/tree primitive %q", required)
		}
	}
}

func TestDashboardSidebarIsFocusedStableAndResponsive(t *testing.T) {
	body := dashboardBody(t)
	for _, required := range []string{
		`<aside id="sidebar" aria-label="Workspace navigation">`,
		`role="tablist" aria-label="Sidebar sections"`,
		`id="conversation-filter" type="search"`,
		`id="attention-section" aria-labelledby="attention-heading" hidden`,
		`attention.hidden=count===0`,
		`function renderStableHTML(`,
		`renderStableHTML(container,html)`,
		`data-action="open-conversation"`,
		`data-conversation-key="${esc(group.key)}"`,
		`class="conversation-runs"`,
		`function renderWorkspace(`,
		`function setExecutionFilter(`,
		`function setSidebarOpen(`,
		`sidebar.inert=!sidebarOpen`,
		`main.inert=sidebarOpen`,
		`setSidebarOpen(false,'main')`,
		`.layout.sidebar-open aside`,
		`.sidebar-backdrop[hidden]{display:none!important}`,
		`.conversation-row{grid-template-columns:minmax(0,1fr) 64px}`,
		`#conversation-filter,#server-form input,#policy-form input{min-height:44px}`,
		`#server-form .row button,#policy-form .row button{min-height:44px}`,
		`class="sidebar-backdrop"`,
		`focusTarget==='sidebar'`,
		`lastFocusedControl===toggle?'sidebar':''`,
		`if(!sidebarOpen)return`,
		`mobileSidebarQuery.addEventListener('change',syncSidebarMode)`,
	} {
		if !strings.Contains(body, required) {
			t.Fatalf("dashboard is missing professional sidebar primitive %q", required)
		}
	}
	for _, obsolete := range []string{
		`grid-template-rows:minmax(150px,36vh)`,
		`What the statuses mean`,
		`Nothing needs your attention`,
		`shown=expanded?js:js.slice(0,12)`,
		`data-action="toggle-conversation"`,
		`class="tabs"`,
		`document.getElementById('tabs')`,
		`groups[s.session_id]`,
	} {
		if strings.Contains(body, obsolete) {
			t.Fatalf("dashboard retained obsolete sidebar behavior %q", obsolete)
		}
	}
	tablistAt := strings.Index(body, `role="tablist" aria-label="Sidebar sections"`)
	panelAt := strings.Index(body, `id="side-conversations" data-sidebar-view="conversations"`)
	if tablistAt < 0 || panelAt < 0 || tablistAt > panelAt {
		t.Fatal("sidebar tablist must precede its tabpanels in keyboard order")
	}
}

func TestDashboardGroupsAndFiltersActivityHistory(t *testing.T) {
	body := dashboardBody(t)
	for _, required := range []string{
		`role="dialog" aria-modal="true" aria-labelledby="history-title"`,
		`data-action="set-audit-filter" data-filter="runs"`,
		`data-action="set-audit-filter" data-filter="attention"`,
		`data-action="set-audit-filter" data-filter="connections"`,
		`data-action="set-audit-filter" data-filter="system"`,
		`function buildAuditGroups(`,
		`jobID=record.job_id||record.data&&record.data.job_id||''`,
		`type==='job.started'&&jobID`,
		`age>10000`,
		`pending.message===(record.message||'')`,
		`group.records.slice(0,20)`,
		`<details class="activity-group">`,
		`function setAuditFilter(`,
		`button.dataset.filter===auditFilter`,
	} {
		if !strings.Contains(body, required) {
			t.Fatalf("dashboard is missing grouped activity history primitive %q", required)
		}
	}
	for _, obsolete := range []string{
		`recs.reverse(); // newest first`,
		`class="arow"`,
		`500 records`,
	} {
		if strings.Contains(body, obsolete) {
			t.Fatalf("dashboard retained noisy activity history behavior %q", obsolete)
		}
	}
}

func TestDashboardApprovalActionsAreExplicitAndRecoverable(t *testing.T) {
	body := dashboardBody(t)
	for _, required := range []string{
		`const approvalState={}`,
		`data-kind="approve"`,
		`Allow once`,
		`data-kind="deny"`,
		`>Deny</button>`,
		`state.busy?'disabled'`,
		`approvalState[id]&&approvalState[id].busy`,
		`state.error?`,
		`approvalState[id]={busy:false,error:`,
		`if(r&&r.error)throw new Error(errorText(r,`,
	} {
		if !strings.Contains(body, required) {
			t.Fatalf("dashboard is missing approval safety primitive %q", required)
		}
	}
}

func TestDashboardPromptAnswersAndChatOutputStaySafeAndBounded(t *testing.T) {
	body := dashboardBody(t)
	for _, required := range []string{
		`function binaryPrompt(`,
		`data-action="answer-prompt"`,
		`data-answer="y"`,
		`data-answer="n"`,
		`function secretPrompt(`,
		`type="${secret?'password':'text'}"`,
		`secret=answer==null&&!!info.promptSecret`,
		`send=info.promptInput||info.onInput`,
		`queueTermInput(info,send,value,{appendNewline:true,secret`,
		`secret:!!o.secret`,
		`secret?'Secret answer hidden'`,
		`appendResponseTurn(info)`,
		`function appendChatOutput(`,
		`function createChatParser(`,
		`function chatParserText(`,
		`!line.isWrapped`,
		`parser._droppedOutput`,
		`const max=160000`,
		`info.output.slice(-max)`,
		`function enforceTranscriptBudget(`,
		`function pruneTranscript(`,
		`function beginOperatorReply(`,
		`function finishResponseTurn(`,
		`boundary.sourceState.dataset.streamState='answered'`,
		`answer not delivered`,
		`function finishChatParser(`,
		`parser.write('',()=>`,
		`renderChatOutputValue(info,out,output,preview)`,
		`enforceTranscriptBudget(info,info.outputEl||out)`,
		`!['completed','unavailable','archived'].includes(state.dataset.streamState)`,
	} {
		if !strings.Contains(body, required) {
			t.Fatalf("dashboard is missing prompt/output safety primitive %q", required)
		}
	}
}

func TestDashboardReconnectsFromCursorAndSurfacesAPIErrors(t *testing.T) {
	body := dashboardBody(t)
	for _, required := range []string{
		`cursor:''`,
		`if(info.cursor)`,
		`encodeURIComponent(info.cursor)`,
		`if(ev.lastEventId) info.cursor=ev.lastEventId`,
		`function archiveTermStream(`,
		`/not[_ ]found/i.test(streamError)`,
		`setCurrentStreamState(info,'archived')`,
		`es.close()`,
		`info.job.stream_available===false`,
		`function currentResponsePart(`,
		`info.streamErrors>=3`,
		`es.onerror=`,
		`if(!r.ok&&!data.error)`,
		`data.error={code:'http_'+r.status`,
		`function errorText(`,
		`class="inline-error"`,
		`role="status" aria-live="polite"`,
	} {
		if !strings.Contains(body, required) {
			t.Fatalf("dashboard is missing reconnect/error primitive %q", required)
		}
	}
}

func TestDashboardPromptUsesOneSafeQueueInBothViews(t *testing.T) {
	body := dashboardBody(t)
	for _, required := range []string{
		`promptInput:(d,o={})=>`,
		`hasOwnProperty.call(m,'job_id')`,
		`info.promptJobID=m.job_id||''`,
		`job_id:jobID`,
		`queueTermInput(info,send,value,`,
		`return next;`,
		`appendNewline:true`,
		`value===''?'Enter pressed'`,
		`notice.dataset.noticeKey===noticeKey`,
		`inputGeneration:0`,
		`function queueRawTermInput(`,
		`function flushRawTermInput(`,
		`TERMINAL.includes(job.status)?''`,
		`controls.hidden=!live`,
		`function setTermInputEnabled(`,
		`info.term.options.disableStdin`,
		`finishChatParser(info,previousTurn)`,
	} {
		if !strings.Contains(body, required) {
			t.Fatalf("dashboard is missing ordered prompt primitive %q", required)
		}
	}
	if strings.Contains(body, `.termhost.mode-terminal .termnotice{display:none}`) {
		t.Fatal("terminal mode hides the interactive prompt")
	}
	if strings.Contains(body, `if(!value)return`) {
		t.Fatal("dashboard refuses an empty Enter response")
	}
}

func TestDashboardLocalizesAndUsesEventDrivenBoundedRefreshes(t *testing.T) {
	body := dashboardBody(t)
	markup := dashboardIndexBody(t)
	if !strings.Contains(markup, `<html lang="en">`) {
		t.Fatal("dashboard does not declare English as its default language")
	}
	if !strings.Contains(body, "const COPY={") || !strings.Contains(body, "termada_locale") {
		t.Fatal("dashboard is missing its compact locale dictionary")
	}
	for _, required := range []string{
		`let refreshInFlight=null,refreshQueued=false`,
		`if(refreshInFlight){refreshQueued=true;return refreshInFlight;}`,
		`refreshOnce().finally(`,
		`/api/dashboard/state?limit=100`,
		`stateRevision`,
		`/api/events?since=`,
		`activeJobsCount>0?2000:45000`,
		`lastJobsOmitted=Number(`,
	} {
		if !strings.Contains(body, required) {
			t.Fatalf("dashboard is missing bounded refresh primitive %q", required)
		}
	}
}

func dashboardBody(t *testing.T) string {
	return dashboardIndexBody(t) + dashboardAsset(t, "assets/app.css") + dashboardAsset(t, "assets/app.js")
}

func dashboardIndexBody(t *testing.T) string {
	t.Helper()
	rr := httptest.NewRecorder()
	Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("dashboard status = %d, want 200", rr.Code)
	}
	return rr.Body.String()
}

func dashboardAsset(t *testing.T, name string) string {
	t.Helper()
	data, err := fs.ReadFile(assets, name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(data)
}
