package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"
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
