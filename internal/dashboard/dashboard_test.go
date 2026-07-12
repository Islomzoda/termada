package dashboard

import (
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func TestHandlerServesNonceCSPWithoutInlineEventHandlers(t *testing.T) {
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
	if !strings.Contains(csp, "script-src 'self' 'nonce-") {
		t.Fatalf("CSP lacks a script nonce: %q", csp)
	}
	scriptDirective := strings.SplitN(strings.SplitN(csp, "script-src ", 2)[1], ";", 2)[0]
	if strings.Contains(scriptDirective, "'unsafe-inline'") {
		t.Fatalf("script-src permits unsafe inline scripts: %q", scriptDirective)
	}
	nonceRE := regexp.MustCompile(`script-src 'self' 'nonce-([^']+)'`)
	match := nonceRE.FindStringSubmatch(csp)
	if len(match) != 2 || !strings.Contains(body, `nonce="`+match[1]+`"`) {
		t.Fatalf("document script nonce does not match CSP")
	}
	if strings.Contains(body, "__CSP_NONCE__") {
		t.Fatal("unexpanded CSP nonce placeholder in response")
	}
	markup := strings.SplitN(body, "<script nonce", 2)[0]
	inlineHandler := regexp.MustCompile(`(?i)\son[a-z]+\s*=`)
	if inlineHandler.MatchString(markup) {
		t.Fatalf("dashboard contains an inline event handler: %q", inlineHandler.FindString(markup))
	}
	if !strings.Contains(body, `replace(/[&<>"']/g`) || !strings.Contains(body, `'"':'&quot;'`) || !strings.Contains(body, `"'":'&#39;'`) {
		t.Fatal("HTML escaping does not cover both attribute quote characters")
	}
	if strings.Contains(body, "localStorage") || !strings.Contains(body, "sessionStorage") {
		t.Fatal("dashboard token must be scoped to the browser tab, not persisted in localStorage")
	}
	for _, required := range []string{
		`class="termnotice"`,
		`aria-live="polite"`,
		`m.awaiting_input`,
		`m.prompt`,
		`m.gap`,
		`queueTermInput`,
		`inputQueue:Promise.resolve()`,
		`!info.inputFailed&&Object.prototype.hasOwnProperty.call`,
	} {
		if !strings.Contains(body, required) {
			t.Fatalf("dashboard is missing interactive prompt support %q", required)
		}
	}

	_, secondBody := request()
	secondNonce := regexp.MustCompile(`nonce="([^"]+)"`).FindStringSubmatch(secondBody)
	if len(secondNonce) != 2 || secondNonce[1] == match[1] {
		t.Fatal("dashboard nonce was not regenerated per response")
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

func TestDashboardContainsDialogTreeAndTerminalFallback(t *testing.T) {
	body := dashboardBody(t)
	for _, required := range []string{
		`function renderDialogTree(`,
		`class="dialog-group"`,
		`class="route"`,
		`/api/exec/list?filter=all`,
		`role="region" aria-label="Хронология запроса"`,
		`aria-live="polite"`,
		`data-view="dialog">Диалог`,
		`data-view="terminal">Терминал`,
		`function setTermView(`,
	} {
		if !strings.Contains(body, required) {
			t.Fatalf("dashboard is missing dialog/tree primitive %q", required)
		}
	}
}

func TestDashboardApprovalActionsAreExplicitAndRecoverable(t *testing.T) {
	body := dashboardBody(t)
	for _, required := range []string{
		`const approvalState={}`,
		`data-kind="approve"`,
		`Разрешить один раз`,
		`data-kind="deny"`,
		`>Отклонить</button>`,
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
		`secret?'Ответ отправлен и скрыт'`,
		`appendResponseTurn(info)`,
		`function appendChatOutput(`,
		`const max=160000`,
		`info.output.slice(-max)`,
		`out.textContent=info.output`,
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
		`value===''?'Нажат Enter'`,
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

func dashboardBody(t *testing.T) string {
	t.Helper()
	rr := httptest.NewRecorder()
	Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("dashboard status = %d, want 200", rr.Code)
	}
	return rr.Body.String()
}
