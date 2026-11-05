package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAPIVersionMigrationErrors(t *testing.T) {
	r := newRegistry(t.TempDir())
	defer r.close()
	handler := withCORS(r, "POST, DELETE, OPTIONS")
	response, mailbox := registrationRequest(t, handler)
	if response.Code != http.StatusOK {
		t.Fatal("current API registration failed")
	}
	for _, path := range []string{
		"/registrations",
		"/registrations/" + mailbox.ID,
		"/registrations/" + mailbox.ID + "/messages",
		"/api/2026-11-04/registrations",
		"/api/2026-11-06/registrations/" + mailbox.ID + "/messages",
		"/api/v2/registrations",
		"/api/2026-11-05-extra/registrations",
		"/api",
	} {
		for _, method := range []string{http.MethodPost, http.MethodDelete, http.MethodOptions} {
			t.Run(method+path, func(t *testing.T) {
				request := httptest.NewRequest(method, path, strings.NewReader("{}"))
				request.Header.Set("Content-Type", "application/json")
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)
				var result struct {
					Error struct{ Code, Message string }
				}
				if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				if response.Code != http.StatusNotFound || result.Error.Code != "unsupported_api_version" || !strings.Contains(result.Error.Message, apiPrefix) {
					t.Fatalf("migration response = %d %+v", response.Code, result)
				}
				if response.Header().Get("Access-Control-Allow-Origin") != "*" || response.Header().Get("Content-Type") != "application/json" {
					t.Fatal("migration error omitted CORS or JSON headers")
				}
				if registrationCount(r) != 1 {
					t.Fatal("unsupported API changed registrations")
				}
				assertDirectoryEntries(t, mailbox.Directory, 0)
			})
		}
	}
}

func TestAPIVersionUnknownPaths(t *testing.T) {
	r := newRegistry(t.TempDir())
	defer r.close()
	handler := withCORS(r, "POST, DELETE, OPTIONS")
	for _, path := range []string{"/unknown", "/apiary/registrations", "/registrations-other", apiPrefix + "/unknown"} {
		for _, method := range []string{http.MethodPost, http.MethodOptions} {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(method, path, nil))
			if method == http.MethodOptions && strings.HasPrefix(path, apiPrefix+"/") {
				if response.Code != http.StatusNoContent {
					t.Fatalf("current preflight = %d", response.Code)
				}
				continue
			}
			if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), `"code":"not_found"`) {
				t.Fatalf("unknown path %s = %d %s", path, response.Code, response.Body.String())
			}
		}
	}
}

func TestAPIRequestPathIsPreserved(t *testing.T) {
	r := newRegistry(t.TempDir())
	defer r.close()
	request := httptest.NewRequest(http.MethodPost, apiPrefix+"/registrations", strings.NewReader("{}"))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	r.ServeHTTP(response, request)
	if response.Code != http.StatusOK || request.URL.Path != apiPrefix+"/registrations" {
		t.Fatalf("request path or response changed unexpectedly: %s, %d", request.URL.Path, response.Code)
	}
}
