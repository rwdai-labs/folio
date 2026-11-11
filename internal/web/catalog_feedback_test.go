package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"folio/internal/store"
)

func TestHTTPActionCatalogAndRun(t *testing.T) {
	backend := &testBackend{}
	server, _ := testServer(t, backend)
	defer server.Close()
	anonymous := &testClient{server: server}
	if status, _, _ := anonymous.request(t, http.MethodGet, "/api/catalog", nil, ""); status != http.StatusUnauthorized {
		t.Fatalf("anonymous catalog status=%d", status)
	}
	if status, _, _ := anonymous.request(t, http.MethodPost, "/api/actions/report/run", map[string]any{"fields": map[string]any{"request": "x"}}, server.URL); status != http.StatusUnauthorized {
		t.Fatalf("anonymous action run status=%d", status)
	}
	client := &testClient{server: server}
	client.login(t, "correct horse")
	status, _, result := client.request(t, http.MethodGet, "/api/catalog", nil, "")
	if status != http.StatusOK {
		t.Fatalf("catalog status=%d result=%v", status, result)
	}
	actions := result["actions"].([]any)
	if len(actions) != 2 || actions[0].(map[string]any)["label_key"] != "catalog.report.label" {
		t.Fatalf("catalog=%v", result)
	}

	csrf := client.csrf
	client.csrf = ""
	if status, _, _ := client.request(t, http.MethodPost, "/api/actions/report/run", map[string]any{"fields": map[string]any{"request": "x"}}, server.URL); status != http.StatusForbidden {
		t.Fatalf("missing CSRF status=%d", status)
	}
	client.csrf = csrf
	status, _, result = client.request(t, http.MethodPost, "/api/actions/report/run", map[string]any{"fields": map[string]any{"request": "Synthetic run"}}, server.URL)
	if status != http.StatusCreated || result["created"] != true {
		t.Fatalf("run status=%d result=%v", status, result)
	}
	if backend.runActionID != "report" || backend.runActionFields["request"] != "Synthetic run" {
		t.Fatalf("run request id=%q fields=%v", backend.runActionID, backend.runActionFields)
	}
	if status, _, result = client.request(t, http.MethodPost, "/api/actions/report/run", map[string]any{"fields": "bad"}, server.URL); status != http.StatusBadRequest || result["error"] != "action_input_invalid" {
		t.Fatalf("bad fields status=%d result=%v", status, result)
	}
	cases := []struct {
		err    string
		status int
		code   string
	}{
		{"action_unknown", http.StatusNotFound, "action_unknown"},
		{"action_disabled", http.StatusConflict, "action_disabled"},
		{"action_input_invalid", http.StatusBadRequest, "action_input_invalid"},
		{"tasks are unavailable", http.StatusInternalServerError, "action_unavailable"},
		{"tasks could not be stored", http.StatusInternalServerError, "action_unavailable"},
	}
	for _, test := range cases {
		backend.runActionErr = errors.New(test.err)
		status, _, result = client.request(t, http.MethodPost, "/api/actions/brief/run", map[string]any{"fields": map[string]any{}}, server.URL)
		if status != test.status || result["error"] != test.code {
			t.Fatalf("%s status=%d result=%v", test.err, status, result)
		}
	}
}

func (c *testClient) multipartRequest(t *testing.T, path string, fields map[string]string, image []byte, csrf bool, origin string) (int, http.Header, []byte, map[string]any) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatal(err)
		}
	}
	if image != nil {
		part, err := writer.CreateFormFile("image", "screenshot.png")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(image); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, c.server.URL+path, &body)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = strings.TrimPrefix(c.server.URL, "http://")
	request.Header.Set("Content-Type", writer.FormDataContentType())
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	if c.cookie != "" {
		request.Header.Set("Cookie", c.cookie)
	}
	if csrf && c.csrf != "" {
		request.Header.Set("X-CSRF-Token", c.csrf)
	}
	response, err := c.server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw := new(bytes.Buffer)
	if _, err := raw.ReadFrom(response.Body); err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	_ = json.Unmarshal(raw.Bytes(), &result)
	return response.StatusCode, response.Header, raw.Bytes(), result
}

func validFeedbackFields() map[string]string {
	return map[string]string{
		"title": "Synthetic feedback", "area": "dashboard",
		"current":    "The synthetic dashboard hides its loading state.",
		"expected":   "The synthetic dashboard shows a bounded loading state.",
		"acceptance": "A synthetic test observes the loading state and its end.",
		"priority":   "P1",
	}
}

func TestHTTPFeedbackMultipartListDetailAndImage(t *testing.T) {
	backend := &testBackend{}
	server, _ := testServer(t, backend)
	defer server.Close()
	anonymous := &testClient{server: server}
	fields := validFeedbackFields()
	if status, _, _, _ := anonymous.multipartRequest(t, "/api/feedback", fields, nil, false, server.URL); status != http.StatusUnauthorized {
		t.Fatalf("anonymous feedback status=%d", status)
	}
	client := &testClient{server: server}
	client.login(t, "correct horse")
	if status, _, _, _ := client.multipartRequest(t, "/api/feedback", fields, nil, false, server.URL); status != http.StatusForbidden {
		t.Fatalf("missing CSRF multipart status=%d", status)
	}
	image := []byte("\x89PNG\r\n\x1a\nsynthetic")
	status, _, _, result := client.multipartRequest(t, "/api/feedback", fields, image, true, server.URL)
	if status != http.StatusCreated || result["accepted"] != true {
		t.Fatalf("feedback status=%d result=%v", status, result)
	}
	record := result["feedback"].(map[string]any)
	id := record["id"].(string)
	if record["has_image"] != true || result["task"] == nil {
		t.Fatalf("feedback result=%v", result)
	}
	if status, _, result = client.request(t, http.MethodGet, "/api/feedback", nil, ""); status != http.StatusOK {
		t.Fatalf("list status=%d result=%v", status, result)
	}
	if len(result["feedback"].([]any)) != 1 {
		t.Fatalf("list result=%v", result)
	}
	status, _, result = client.request(t, http.MethodGet, "/api/feedback/"+id, nil, "")
	if status != http.StatusOK || result["feedback"].(map[string]any)["title"] != "Synthetic feedback" {
		t.Fatalf("detail status=%d result=%v", status, result)
	}
	status, headers, raw, _ := client.multipartGet(t, "/api/feedback/"+id+"/image")
	if status != http.StatusOK || !bytes.Equal(raw, image) {
		t.Fatalf("image status=%d bytes=%v", status, raw)
	}
	if headers.Get("Content-Type") != "image/png" || headers.Get("Cache-Control") != "no-store" ||
		headers.Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(headers.Get("Content-Disposition"), "inline") {
		t.Fatalf("image headers=%v", headers)
	}
	if status, _, _ = client.request(t, http.MethodGet, "/api/feedback/f-aaaaaaaaaaaa", nil, ""); status != http.StatusNotFound {
		t.Fatalf("unknown detail status=%d", status)
	}
	if status, _, _ = client.request(t, http.MethodGet, "/api/feedback/f-aaaaaaaaaaaa/image", nil, ""); status != http.StatusNotFound {
		t.Fatalf("unknown image status=%d", status)
	}
	if status, _, _ = anonymous.request(t, http.MethodGet, "/api/feedback", nil, ""); status != http.StatusUnauthorized {
		t.Fatalf("anonymous list status=%d", status)
	}
	if status, _, _ = anonymous.request(t, http.MethodGet, "/api/feedback/"+id+"/image", nil, ""); status != http.StatusUnauthorized {
		t.Fatalf("anonymous image status=%d", status)
	}

	backend.feedbackRejected = true
	backend.feedbackReason = "too_broad"
	status, _, _, result = client.multipartRequest(t, "/api/feedback", fields, nil, true, server.URL)
	if status != http.StatusOK || result["accepted"] != false || result["feedback"].(map[string]any)["reason"] != "too_broad" {
		t.Fatalf("rejected status=%d result=%v", status, result)
	}
	backend.feedbackRejected = false
	invalid := validFeedbackFields()
	delete(invalid, "title")
	status, _, _, result = client.multipartRequest(t, "/api/feedback", invalid, nil, true, server.URL)
	if status != http.StatusBadRequest || result["error"] != "feedback_invalid" || result["reason"] != "title_short" {
		t.Fatalf("invalid status=%d result=%v", status, result)
	}
	for _, reason := range []string{"area_invalid", "current_short", "expected_short", "acceptance_short", "priority_invalid", "image_invalid", "too_large", "too_long"} {
		backend.feedbackErr = errors.New(reason)
		status, _, _, result = client.multipartRequest(t, "/api/feedback", fields, nil, true, server.URL)
		if status != http.StatusBadRequest || result["error"] != "feedback_invalid" || result["reason"] != reason {
			t.Fatalf("reason %s status=%d result=%v", reason, status, result)
		}
	}
	backend.feedbackErr = errors.New("feedback_unavailable")
	status, _, _, result = client.multipartRequest(t, "/api/feedback", fields, nil, true, server.URL)
	if status != http.StatusInternalServerError || result["error"] != "feedback_unavailable" {
		t.Fatalf("internal feedback failure status=%d result=%v", status, result)
	}
	backend.feedbackErr = nil
	if status, _, result = client.request(t, http.MethodPost, "/api/feedback", map[string]any{}, server.URL); status != http.StatusBadRequest || result["error"] != "feedback_invalid" {
		t.Fatalf("json feedback status=%d result=%v", status, result)
	}
}

func (c *testClient) multipartGet(t *testing.T, path string) (int, http.Header, []byte, map[string]any) {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, c.server.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = strings.TrimPrefix(c.server.URL, "http://")
	if c.cookie != "" {
		request.Header.Set("Cookie", c.cookie)
	}
	response, err := c.server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw := new(bytes.Buffer)
	if _, err := raw.ReadFrom(response.Body); err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	_ = json.Unmarshal(raw.Bytes(), &result)
	return response.StatusCode, response.Header, raw.Bytes(), result
}

func TestHTTPFeedbackContextMultipartDetailListAndRejection(t *testing.T) {
	data, tools := t.TempDir(), t.TempDir()
	backend, err := store.New(data, tools, nil)
	if err != nil {
		t.Fatal(err)
	}
	passwordFile := filepath.Join(data, "synthetic-password")
	if err := os.WriteFile(passwordFile, []byte("correct horse"), 0o600); err != nil {
		t.Fatal(err)
	}
	handler, err := New(backend, Options{DataDir: data, PasswordFile: passwordFile, AssetsDir: tools, TrustedHosts: []string{"127.0.0.1"}})
	if err != nil {
		t.Fatal(err)
	}
	defer handler.auth.db.Close()
	server := httptest.NewServer(handler)
	defer server.Close()
	client := &testClient{server: server}
	client.login(t, "correct horse")

	contextJSON := `{"view":"dashboard","route":"/#dashboard","theme":"light","language":"en","viewport":{"width":1440,"height":900,"dpr":1},"elements":[{"index":1,"tag":"button","id":"retry-task","role":"button","name":"Retry","text":"Retry","selector":"#retry-task","section":"Task card","rect":{"x":100,"y":200,"width":80,"height":32}}]}`
	fields := validFeedbackFields()
	fields["context"] = contextJSON
	status, _, _, result := client.multipartRequest(t, "/api/feedback", fields, nil, true, server.URL)
	if status != http.StatusCreated || result["accepted"] != true {
		t.Fatalf("context feedback status=%d result=%v", status, result)
	}
	record := result["feedback"].(map[string]any)
	id := record["id"].(string)
	contextValue, ok := record["context"].(map[string]any)
	if !ok || contextValue["view"] != "dashboard" || contextValue["route"] != "/#dashboard" {
		t.Fatalf("created context=%v", record["context"])
	}
	elements := contextValue["elements"].([]any)
	if len(elements) != 1 || elements[0].(map[string]any)["selector"] != "#retry-task" {
		t.Fatalf("created elements=%v", elements)
	}

	status, _, detail := client.request(t, http.MethodGet, "/api/feedback/"+id, nil, "")
	if status != http.StatusOK || detail["feedback"].(map[string]any)["context"].(map[string]any)["viewport"].(map[string]any)["width"] != float64(1440) {
		t.Fatalf("detail status=%d result=%v", status, detail)
	}
	status, _, listing := client.request(t, http.MethodGet, "/api/feedback", nil, "")
	if status != http.StatusOK {
		t.Fatalf("list status=%d result=%v", status, listing)
	}
	item := findFeedbackSummaryByID(t, listing, id)
	if item["element_count"] != float64(1) {
		t.Fatalf("summary element_count=%v", item["element_count"])
	}
	if _, exists := item["context"]; exists {
		t.Fatalf("summary leaked context: %v", item)
	}
	if _, exists := item["elements"]; exists {
		t.Fatalf("summary leaked elements: %v", item)
	}

	noContext := validFeedbackFields()
	status, _, _, result = client.multipartRequest(t, "/api/feedback", noContext, nil, true, server.URL)
	if status != http.StatusCreated {
		t.Fatalf("no-context status=%d result=%v", status, result)
	}
	noContextID := result["feedback"].(map[string]any)["id"].(string)
	status, _, detail = client.request(t, http.MethodGet, "/api/feedback/"+noContextID, nil, "")
	if status != http.StatusOK {
		t.Fatalf("no-context detail status=%d result=%v", status, detail)
	}
	if _, exists := detail["feedback"].(map[string]any)["context"]; exists {
		t.Fatalf("no-context detail invented context: %v", detail)
	}
	status, _, listing = client.request(t, http.MethodGet, "/api/feedback", nil, "")
	if status != http.StatusOK || findFeedbackSummaryByID(t, listing, noContextID)["element_count"] != float64(0) {
		t.Fatalf("no-context list status=%d result=%v", status, listing)
	}

	tooManyElements := `{"view":"dashboard","viewport":{"width":1440,"height":900,"dpr":1},"elements":[`
	for index := 0; index < 25; index++ {
		if index > 0 {
			tooManyElements += ","
		}
		tooManyElements += fmt.Sprintf(`{"index":%d,"tag":"button","rect":{"x":0,"y":0,"width":1,"height":1}}`, index+1)
	}
	tooManyElements += `]}`
	invalid := []struct {
		name    string
		context string
	}{
		{"unknown key", `{"view":"dashboard","viewport":{"width":1440,"height":900,"dpr":1},"html":"<div>"}`},
		{"duplicate key", `{"view":"dashboard","view":"assistant","viewport":{"width":1440,"height":900,"dpr":1}}`},
		{"too many elements", tooManyElements},
		{"invalid json", `{"view":"dashboard",`},
		{"unknown view", `{"view":"home","viewport":{"width":1440,"height":900,"dpr":1}}`},
		{"oversized", strings.Repeat(" ", 24*1024+1)},
		{"empty", ""},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			fields := validFeedbackFields()
			fields["context"] = test.context
			status, _, _, result := client.multipartRequest(t, "/api/feedback", fields, nil, true, server.URL)
			if status != http.StatusBadRequest || result["error"] != "feedback_invalid" || result["reason"] != "context_invalid" {
				t.Fatalf("status=%d result=%v", status, result)
			}
		})
	}
	status, _, listing = client.request(t, http.MethodGet, "/api/feedback", nil, "")
	if status != http.StatusOK || len(listing["feedback"].([]any)) != 2 {
		t.Fatalf("rejected context created feedback: %d %v", status, listing)
	}
}

func findFeedbackSummaryByID(t *testing.T, listing map[string]any, id string) map[string]any {
	t.Helper()
	for _, item := range listing["feedback"].([]any) {
		summary := item.(map[string]any)
		if summary["id"] == id {
			return summary
		}
	}
	t.Fatalf("feedback %s not listed: %v", id, listing)
	return nil
}

func TestHTTPFeedbackAnswersRoute(t *testing.T) {
	backend := &testBackend{}
	server, _ := testServer(t, backend)
	defer server.Close()
	client := &testClient{server: server}
	client.login(t, "correct horse")
	status, _, _, result := client.multipartRequest(t, "/api/feedback", validFeedbackFields(), nil, true, server.URL)
	if status != http.StatusCreated {
		t.Fatalf("create status=%d result=%v", status, result)
	}
	id := result["feedback"].(map[string]any)["id"].(string)
	answers := map[string]any{"answers": []any{map[string]any{"question_id": "q1", "option_id": "o1"}}}

	anonymous := &testClient{server: server}
	if status, _, _ = anonymous.request(t, http.MethodPost, "/api/feedback/"+id+"/answers", answers, server.URL); status != http.StatusUnauthorized {
		t.Fatalf("anonymous answers status=%d", status)
	}
	noCSRF := &testClient{server: server, cookie: client.cookie}
	if status, _, _ = noCSRF.request(t, http.MethodPost, "/api/feedback/"+id+"/answers", answers, server.URL); status != http.StatusForbidden {
		t.Fatalf("missing csrf answers status=%d", status)
	}
	if status, _, _ = client.request(t, http.MethodPost, "/api/feedback/f-aaaaaaaaaaaa/answers", answers, server.URL); status != http.StatusNotFound {
		t.Fatalf("unknown answers status=%d", status)
	}
	if status, _, _ = client.request(t, http.MethodPost, "/api/feedback/not-an-id/answers", answers, server.URL); status != http.StatusNotFound {
		t.Fatalf("malformed answers status=%d", status)
	}
	status, _, result = client.request(t, http.MethodPost, "/api/feedback/"+id+"/answers", answers, server.URL)
	if status != http.StatusOK || result["feedback"].(map[string]any)["status"] != "accepted" {
		t.Fatalf("answers status=%d result=%v", status, result)
	}
	status, _, result = client.request(t, http.MethodPost, "/api/feedback/"+id+"/answers", map[string]any{}, server.URL)
	if status != http.StatusBadRequest || result["error"] != "feedback_invalid" || result["reason"] != "answers_invalid" {
		t.Fatalf("invalid answers status=%d result=%v", status, result)
	}
	backend.feedbackAnswerErr = errors.New("feedback_not_awaiting_answers")
	status, _, result = client.request(t, http.MethodPost, "/api/feedback/"+id+"/answers", answers, server.URL)
	if status != http.StatusConflict || result["error"] != "feedback_not_awaiting_answers" {
		t.Fatalf("not awaiting status=%d result=%v", status, result)
	}
	backend.feedbackAnswerErr = nil
	backend.feedbackErr = errors.New("feedback_unavailable")
	status, _, result = client.request(t, http.MethodPost, "/api/feedback/"+id+"/answers", answers, server.URL)
	if status != http.StatusInternalServerError || result["error"] != "feedback_unavailable" {
		t.Fatalf("unavailable status=%d result=%v", status, result)
	}
}
