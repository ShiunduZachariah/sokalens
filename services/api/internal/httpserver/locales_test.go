package httpserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLocalesHandlerUsesBrowserBeforeCountryHint(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/v1/locales?country=DE", nil)
	request.Header.Set("Accept-Language", "fr-FR,fr;q=0.9")
	recorder := httptest.NewRecorder()
	accountDependencies{}.localesHandler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if !strings.Contains(body, `"locale":"fr"`) || !strings.Contains(body, `"source":"browser"`) || !strings.Contains(body, `"country_is_hint":true`) {
		t.Fatalf("response=%s", body)
	}
}

func TestLocalesHandlerUsesGuestPreference(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/v1/locales?country=DE", nil)
	request.AddCookie(&http.Cookie{Name: localeCookie, Value: "es"})
	request.Header.Set("Accept-Language", "fr")
	recorder := httptest.NewRecorder()
	accountDependencies{}.localesHandler().ServeHTTP(recorder, request)
	if !strings.Contains(recorder.Body.String(), `"locale":"es"`) || !strings.Contains(recorder.Body.String(), `"source":"guest"`) {
		t.Fatalf("response=%s", recorder.Body.String())
	}
}
