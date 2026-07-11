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
