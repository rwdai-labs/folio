package store

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func feedbackPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.SetRGBA(x, y, color.RGBA{R: uint8(x % 255), G: uint8(y % 255), B: 80, A: 255})
		}
	}
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, img); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func feedbackJPEG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.SetRGBA(x, y, color.RGBA{R: 120, G: uint8(x % 255), B: uint8(y % 255), A: 255})
		}
	}
	var buffer bytes.Buffer
	if err := jpeg.Encode(&buffer, img, &jpeg.Options{Quality: 80}); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func validFeedbackInput() FeedbackInput {
	return FeedbackInput{
		Title: "Synthetic feedback", Area: "dashboard",
		Current:    "The synthetic dashboard hides its loading state.",
		Expected:   "The synthetic dashboard shows a bounded loading state.",
		Acceptance: "A synthetic test observes the loading state and its end.",
		Priority:   "P1",
	}
}

func TestCreateFeedbackAcceptsWithoutModelBinding(t *testing.T) {
	store := newTestStore(t)
	value, err := store.CreateFeedback(validFeedbackInput(), nil)
	if err != nil {
		t.Fatalf("create feedback: %v", err)
	}
	if value["accepted"] != true {
		t.Fatalf("feedback not accepted: %v", value)
	}
	record := value["feedback"].(map[string]any)
	id := record["id"].(string)
	if !feedbackIDRE.MatchString(id) || record["status"] != "accepted" || record["reason"] != nil {
		t.Fatalf("feedback record=%v", record)
	}
	taskID, _ := record["task_id"].(string)
	if !taskIDRE.MatchString(taskID) {
		t.Fatalf("task link=%q", taskID)
	}
	dirInfo, err := os.Stat(store.feedbackDirFor(id))
	if err != nil || dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("feedback dir mode=%v err=%v", dirInfo, err)
	}
	recordInfo, err := os.Stat(filepath.Join(store.feedbackDirFor(id), "feedback.json"))
	if err != nil || recordInfo.Mode().Perm() != 0o600 {
		t.Fatalf("feedback file mode=%v err=%v", recordInfo, err)
	}
	if _, err := os.Stat(filepath.Join(store.feedbackDirFor(id), "screenshot.png")); !os.IsNotExist(err) {
		t.Fatal("feedback without an image stored a screenshot")
	}
	tasks, err := store.ListTasks()
	if err != nil {
		t.Fatal(err)
	}
	var task map[string]any
	for _, item := range tasks["tasks"].([]any) {
		candidate := item.(map[string]any)
		if candidate["id"] == taskID {
			task = candidate
			break
		}
	}
	if task == nil || task["kind"] != taskKindFeedbackPlan || task["source"] != taskSourceFeedback || task["priority"] != "P1" {
		t.Fatalf("planning task=%v", task)
	}
	if !strings.HasPrefix(task["title"].(string), "Plan: ") || len([]rune(task["title"].(string))) > maxTaskTitleRunes {
		t.Fatalf("planning title=%q", task["title"])
	}
	request := task["payload"].(map[string]any)["request"].(string)
	for _, phrase := range []string{"plan-only", "do not edit files", "do not deploy", "likely files", "tests", "ambiguities", "scope risks", "Do not ask clarifying questions"} {
		if !strings.Contains(request, phrase) {
			t.Fatalf("planning prompt missing %q: %s", phrase, request)
		}
	}
	if len([]rune(request)) > feedbackPlanPromptRunes {
		t.Fatalf("planning prompt=%d runes", len([]rune(request)))
	}
	detail, err := store.FeedbackDetail(id)
	if err != nil {
		t.Fatal(err)
	}
	if detail["feedback"].(map[string]any)["current"] != validFeedbackInput().Current {
		t.Fatalf("detail=%v", detail)
	}
	listing, err := store.ListFeedback()
	if err != nil {
		t.Fatal(err)
	}
	items := listing["feedback"].([]any)
	if len(items) != 1 {
		t.Fatalf("feedback list=%v", items)
	}
	if _, ok := items[0].(map[string]any)["current"]; ok {
		t.Fatalf("summary leaked full text: %v", items[0])
	}
}

func TestCreateFeedbackDeterministicReasons(t *testing.T) {
	store := newTestStore(t)
	cases := []struct {
		name   string
		mutate func(*FeedbackInput)
		image  []byte
		want   string
	}{
		{"title short", func(input *FeedbackInput) { input.Title = "abc" }, nil, "title_short"},
		{"title long", func(input *FeedbackInput) { input.Title = strings.Repeat("x", 121) }, nil, "title_long"},
		{"area invalid", func(input *FeedbackInput) { input.Area = "home" }, nil, "area_invalid"},
		{"current empty", func(input *FeedbackInput) { input.Current = "" }, nil, "current_short"},
		{"current long", func(input *FeedbackInput) { input.Current = strings.Repeat("x", maxFeedbackTextRunes+1) }, nil, "too_long"},
		{"expected empty", func(input *FeedbackInput) { input.Expected = "" }, nil, "expected_short"},
		{"expected long", func(input *FeedbackInput) { input.Expected = strings.Repeat("x", maxFeedbackTextRunes+1) }, nil, "too_long"},
		{"acceptance empty", func(input *FeedbackInput) { input.Acceptance = "" }, nil, "acceptance_short"},
		{"acceptance long", func(input *FeedbackInput) { input.Acceptance = strings.Repeat("x", maxFeedbackTextRunes+1) }, nil, "too_long"},
		{"priority invalid", func(input *FeedbackInput) { input.Priority = "P3" }, nil, "priority_invalid"},
		{"image invalid", func(input *FeedbackInput) {}, []byte("not an image"), "image_invalid"},
		{"image too large", func(input *FeedbackInput) {}, make([]byte, maxFeedbackBytes+1), "too_large"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			input := validFeedbackInput()
			test.mutate(&input)
			if _, err := store.CreateFeedback(input, test.image); err == nil || err.Error() != test.want {
				t.Fatalf("err=%v want %s", err, test.want)
			}
		})
	}
	listing, err := store.ListFeedback()
	if err != nil {
		t.Fatal(err)
	}
	if len(listing["feedback"].([]any)) != 0 {
		t.Fatalf("malformed input created feedback: %v", listing)
	}
}

func TestCreateFeedbackMaxBoundedInputStaysUnderPlanLimit(t *testing.T) {
	store := newTestStore(t)
	input := validFeedbackInput()
	input.Title = strings.Repeat("t", 120)
	input.Current = strings.Repeat("c", 1000)
	input.Expected = strings.Repeat("e", 1000)
	input.Acceptance = strings.Repeat("a", 1000)
	if runes := len([]rune(feedbackPlanPrompt(input))); runes > feedbackPlanPromptRunes {
		t.Fatalf("bounded input produced %d-rune prompt", runes)
	}
	value, err := store.CreateFeedback(input, nil)
	if err != nil {
		t.Fatalf("bounded feedback: %v", err)
	}
	if value["accepted"] != true {
		t.Fatalf("bounded feedback rejected: %v", value)
	}
	record := value["feedback"].(map[string]any)
	request := ""
	tasks, err := store.ListTasks()
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range tasks["tasks"].([]any) {
		task := item.(map[string]any)
		if task["id"] == record["task_id"] {
			request = task["payload"].(map[string]any)["request"].(string)
		}
	}
	if request == "" || len([]rune(request)) > feedbackPlanPromptRunes {
		t.Fatalf("planning request length=%d", len([]rune(request)))
	}
}

func TestFeedbackImageReencodeBoundsAndSafety(t *testing.T) {
	store := newTestStore(t)
	value, err := store.CreateFeedback(validFeedbackInput(), feedbackPNG(t, 64, 64))
	if err != nil {
		t.Fatalf("png feedback: %v", err)
	}
	record := value["feedback"].(map[string]any)
	id := record["id"].(string)
	if record["has_image"] != true || record["image_bytes"].(int) <= 0 {
		t.Fatalf("image metadata=%v", record)
	}
	raw, err := store.FeedbackImage(id)
	if err != nil || !bytes.HasPrefix(raw, []byte("\x89PNG\r\n\x1a\n")) {
		t.Fatalf("stored image not PNG: %v", err)
	}
	info, err := os.Stat(filepath.Join(store.feedbackDirFor(id), "screenshot.png"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("screenshot mode=%v err=%v", info, err)
	}

	jpegValue, err := store.CreateFeedback(validFeedbackInput(), feedbackJPEG(t, 64, 64))
	if err != nil {
		t.Fatalf("jpeg feedback: %v", err)
	}
	jpegID := jpegValue["feedback"].(map[string]any)["id"].(string)
	jpegRaw, err := store.FeedbackImage(jpegID)
	if err != nil || !bytes.HasPrefix(jpegRaw, []byte("\x89PNG\r\n\x1a\n")) {
		t.Fatalf("jpeg was not re-encoded as PNG: %v", err)
	}

	for _, size := range [][2]int{{32, 64}, {5000, 64}} {
		if _, err := store.CreateFeedback(validFeedbackInput(), feedbackPNG(t, size[0], size[1])); err == nil || err.Error() != "image_invalid" {
			t.Fatalf("size %v err=%v", size, err)
		}
	}

	if _, err := store.FeedbackDetail("../escape"); err == nil || err.Error() != "feedback_unknown" {
		t.Fatalf("path traversal detail err=%v", err)
	}
	if _, err := store.FeedbackImage("f-aaaaaaaaaaaa"); err == nil || err.Error() != "feedback_unknown" {
		t.Fatalf("unknown image err=%v", err)
	}
	if _, err := store.CreateFeedback(validFeedbackInput(), nil); err != nil {
		t.Fatal(err)
	}
	noImage, _ := store.CreateFeedback(validFeedbackInput(), nil)
	noImageID := noImage["feedback"].(map[string]any)["id"].(string)
	if _, err := store.FeedbackImage(noImageID); err == nil || err.Error() != "feedback_image_unavailable" {
		t.Fatalf("no-image err=%v", err)
	}

	outside := filepath.Join(t.TempDir(), "outside.png")
	if err := os.WriteFile(outside, feedbackPNG(t, 64, 64), 0o600); err != nil {
		t.Fatal(err)
	}
	imagePath := filepath.Join(store.feedbackDirFor(id), "screenshot.png")
	if err := os.Remove(imagePath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, imagePath); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FeedbackImage(id); err == nil {
		t.Fatal("symlinked screenshot was read")
	}
}

func TestFeedbackGateIsAdvisory(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		status   int
		advisory string
	}{
		{"ready fenced", "```json\n{\"ready\":true,\"reason\":\"ok\"}\n```", http.StatusOK, ""},
		{"ready with unusual reason", "{\"ready\":true,\"reason\":\"clear\"}", http.StatusOK, ""},
		{"ready with reject reason", "{\"ready\":true,\"reason\":\"too_broad\"}", http.StatusOK, ""},
		{"reject", "{\"ready\":false,\"reason\":\"too_broad\"}", http.StatusOK, "too_broad"},
		{"unknown reason", "{\"ready\":false,\"reason\":\"maybe\"}", http.StatusOK, "ambiguous"},
		{"extra field", "{\"ready\":true,\"reason\":\"ok\",\"extra\":1}", http.StatusOK, "gate_unavailable"},
		{"wrong type", "{\"ready\":\"yes\",\"reason\":\"ok\"}", http.StatusOK, "gate_unavailable"},
		{"prose no object", "the request is fine", http.StatusOK, "gate_unavailable"},
		{"upstream failure", "", http.StatusInternalServerError, "gate_unavailable"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			store := newTestStore(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var payload map[string]any
				_ = json.NewDecoder(r.Body).Decode(&payload)
				if payload["max_tokens"] != float64(512) {
					t.Errorf("gate request max_tokens=%v", payload["max_tokens"])
				}
				if test.status != http.StatusOK {
					w.WriteHeader(test.status)
					return
				}
				_, _ = w.Write([]byte(`{"choices":[{"message":{"content":` + string(jsonText(t, test.body)) + `},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120,"prompt_tokens_details":{"cached_tokens":30}}}`))
			}))
			defer server.Close()
			bindSyntheticService(t, store, server.URL, map[string]any{"report": syntheticBinding("group/gate")}, nil)
			value, err := store.CreateFeedback(validFeedbackInput(), nil)
			if err != nil {
				t.Fatalf("gate call: %v", err)
			}
			// The gate never vetoes owner input: a submission is always accepted
			// and only carries the model's opinion as bounded advisory metadata.
			if value["accepted"] != true {
				t.Fatalf("accepted=%v want true value=%v", value["accepted"], value)
			}
			record := value["feedback"].(map[string]any)
			if record["status"] != "accepted" || record["task_id"] == nil {
				t.Fatalf("accepted record=%v", record)
			}
			if record["reason"] != nil {
				t.Fatalf("advisory verdict was recorded as a rejection reason: %v", record)
			}
			advisory, _ := record["gate_advisory"].(string)
			if advisory != test.advisory {
				t.Fatalf("gate_advisory=%q want %q value=%v", advisory, test.advisory, record)
			}
			persisted, err := os.ReadFile(filepath.Join(store.feedbackDirFor(record["id"].(string)), "feedback.json"))
			if err != nil {
				t.Fatal(err)
			}
			var stored map[string]any
			if err := json.Unmarshal(persisted, &stored); err != nil {
				t.Fatal(err)
			}
			if storedAdvisory, _ := stored["gate_advisory"].(string); storedAdvisory != test.advisory {
				t.Fatalf("persisted gate_advisory=%q want %q", storedAdvisory, test.advisory)
			}
			if test.status == http.StatusOK {
				usage, ok := record["gate_usage"].(map[string]any)
				if !ok || usage["input_tokens"] != int64(70) || usage["cached_tokens"] != int64(30) || usage["total_tokens"] != int64(120) {
					t.Fatalf("gate usage=%v", record["gate_usage"])
				}
				if storedUsage, ok := stored["gate_usage"].(map[string]any); !ok || storedUsage["total_tokens"] != float64(120) {
					t.Fatalf("persisted gate usage=%v", stored["gate_usage"])
				}
			} else if record["gate_usage"] != nil || stored["gate_usage"] != nil {
				t.Fatalf("failed call without a response invented usage: %v", record)
			}
			if strings.Contains(string(persisted), "maybe") || strings.Contains(string(persisted), "the request is fine") {
				t.Fatalf("raw gate output persisted: %s", persisted)
			}
		})
	}
}

func jsonText(t *testing.T, text string) []byte {
	t.Helper()
	raw, err := json.Marshal(text)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestFeedbackPlanRequiresDedicatedFeedbackSource(t *testing.T) {
	store := newTestStore(t)
	for _, source := range []string{"manual", "agent:console-main", "hook:synthetic", "event:synthetic", "action:parent"} {
		if _, err := store.CreateTask(taskKindFeedbackPlan, source, "Bypass", "P1", map[string]any{"request": "Synthetic"}); err == nil || err.Error() != "invalid_task_kind" {
			t.Fatalf("reserved kind from %s err=%v", source, err)
		}
	}
	request := strings.Repeat("界", feedbackPlanPromptRunes)
	result, err := store.CreateTask(taskKindFeedbackPlan, taskSourceFeedback, "Plan: synthetic", "P1", map[string]any{"request": request})
	if err != nil {
		t.Fatalf("dedicated feedback plan: %v", err)
	}
	task := result["task"].(map[string]any)
	if task["source"] != taskSourceFeedback || task["payload"].(map[string]any)["request"] != request {
		t.Fatalf("feedback plan task=%v", task)
	}
	id := task["id"].(string)
	store.taskMu.Lock()
	store.tasksCache = nil
	file, err := store.loadTasksLocked()
	store.taskMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	for _, loaded := range file.Tasks {
		if loaded.ID != id {
			continue
		}
		if loaded.Payload["request"] != request {
			t.Fatalf("feedback plan payload did not survive reload: %d runes", len([]rune(loaded.Payload["request"].(string))))
		}
		if snapshot := snapshotTaskPayload(loaded); snapshot["request"] != request {
			t.Fatalf("feedback plan snapshot dropped request: %d runes", len([]rune(snapshot["request"].(string))))
		}
		return
	}
	t.Fatalf("feedback plan task %s was dropped on reload", id)
}

func syntheticPageContextFixture() FeedbackContext {
	return FeedbackContext{
		View: "dashboard", Route: "/#dashboard", Theme: "light", Language: "en",
		Viewport: FeedbackViewport{Width: 1440, Height: 900, DPR: 1},
		Elements: []FeedbackElement{
			{
				Index: 1, Tag: "button", ID: "retry-task", Role: "button",
				Name: "Retry", Text: "Retry", Selector: "#retry-task",
				Section: "Task card", Rect: FeedbackRect{X: 100, Y: 200, Width: 80, Height: 32},
			},
			{
				Index: 2, Tag: "span", ID: "task-status", Role: "status",
				Name: "Task status", Text: "Running", Selector: "#task-status",
				Section: "Task card", Rect: FeedbackRect{X: 100, Y: 240, Width: 120, Height: 20},
			},
		},
	}
}

func encodePageContextFixture(t *testing.T, context FeedbackContext) string {
	t.Helper()
	raw, err := json.Marshal(context)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func pageContextRequestValues(context string) map[string]any {
	input := validFeedbackInput()
	values := map[string]any{
		"title": input.Title, "area": input.Area, "current": input.Current,
		"expected": input.Expected, "acceptance": input.Acceptance, "priority": input.Priority,
	}
	if context != "" {
		values["context"] = context
	}
	return values
}

func TestFeedbackContextPersistencePublicAndList(t *testing.T) {
	store := newTestStore(t)
	context := syntheticPageContextFixture()
	value, err := store.CreateFeedbackRequest(pageContextRequestValues(encodePageContextFixture(t, context)), nil)
	if err != nil {
		t.Fatalf("create feedback with context: %v", err)
	}
	if value["accepted"] != true {
		t.Fatalf("feedback not accepted: %v", value)
	}
	record := value["feedback"].(map[string]any)
	id := record["id"].(string)
	public, ok := record["context"].(map[string]any)
	if !ok || public["view"] != "dashboard" || public["route"] != "/#dashboard" ||
		public["theme"] != "light" || public["language"] != "en" {
		t.Fatalf("public context=%v", record["context"])
	}
	elements, ok := public["elements"].([]any)
	if !ok || len(elements) != 2 {
		t.Fatalf("public elements=%v", public["elements"])
	}
	first := elements[0].(map[string]any)
	if first["tag"] != "button" || first["name"] != "Retry" || first["selector"] != "#retry-task" {
		t.Fatalf("public element=%v", first)
	}
	persisted, err := os.ReadFile(filepath.Join(store.feedbackDirFor(id), "feedback.json"))
	if err != nil {
		t.Fatal(err)
	}
	var stored map[string]any
	if err := json.Unmarshal(persisted, &stored); err != nil {
		t.Fatal(err)
	}
	if stored["context"] == nil {
		t.Fatalf("context was not persisted: %s", persisted)
	}
	if _, err := os.Stat(filepath.Join(store.feedbackDirFor(id), "screenshot.png")); !os.IsNotExist(err) {
		t.Fatal("context created a screenshot")
	}
	tasks, err := store.ListTasks()
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range tasks["tasks"].([]any) {
		task := item.(map[string]any)
		if task["id"] != record["task_id"] {
			continue
		}
		request := task["payload"].(map[string]any)["request"].(string)
		if !strings.Contains(request, "Page context (untrusted data)") ||
			!strings.Contains(request, "view=dashboard") ||
			!strings.Contains(request, `selector="#retry-task"`) {
			t.Fatalf("plan prompt omitted context summary: %s", request)
		}
	}

	detail, err := store.FeedbackDetail(id)
	if err != nil {
		t.Fatal(err)
	}
	detailContext, ok := detail["feedback"].(map[string]any)["context"].(map[string]any)
	if !ok || len(detailContext["elements"].([]any)) != 2 {
		t.Fatalf("detail context=%v", detail["feedback"])
	}
	listing, err := store.ListFeedback()
	if err != nil {
		t.Fatal(err)
	}
	items := listing["feedback"].([]any)
	if len(items) != 1 {
		t.Fatalf("feedback list=%v", items)
	}
	summary := items[0].(map[string]any)
	if summary["element_count"] != 2 {
		t.Fatalf("summary element_count=%v", summary["element_count"])
	}
	if _, exists := summary["context"]; exists {
		t.Fatalf("summary leaked context: %v", summary)
	}
	if _, exists := summary["elements"]; exists {
		t.Fatalf("summary leaked elements: %v", summary)
	}
}

func TestFeedbackContextRejectsInvalid(t *testing.T) {
	store := newTestStore(t)
	tooMany := syntheticPageContextFixture()
	tooMany.Elements = make([]FeedbackElement, maxFeedbackContextElements+1)
	for index := range tooMany.Elements {
		tooMany.Elements[index] = FeedbackElement{Index: index + 1, Tag: "button", Rect: FeedbackRect{}}
	}
	longTag := syntheticPageContextFixture()
	longTag.Elements[0].Tag = strings.Repeat("t", maxFeedbackContextTagRunes+1)
	longRoute := syntheticPageContextFixture()
	longRoute.Route = strings.Repeat("r", maxFeedbackContextRouteRunes+1)
	longText := syntheticPageContextFixture()
	longText.Elements[0].Text = strings.Repeat("x", maxFeedbackContextTextRunes+1)
	longSelector := syntheticPageContextFixture()
	longSelector.Elements[0].Selector = strings.Repeat("s", maxFeedbackContextSelectorRunes+1)
	longSection := syntheticPageContextFixture()
	longSection.Elements[0].Section = strings.Repeat("q", maxFeedbackContextSectionRunes+1)
	invalidRect := syntheticPageContextFixture()
	invalidRect.Elements[0].Rect.X = maxFeedbackContextRectValue + 1
	invalidViewport := syntheticPageContextFixture()
	invalidViewport.Viewport.Width = 0
	invalidDPR := syntheticPageContextFixture()
	invalidDPR.Viewport.DPR = maxFeedbackViewportDPR + 0.5
	invalidView := syntheticPageContextFixture()
	invalidView.View = "home"
	invalidTheme := syntheticPageContextFixture()
	invalidTheme.Theme = "sepia"
	invalidLanguage := syntheticPageContextFixture()
	invalidLanguage.Language = "fr"
	cases := []struct {
		name    string
		context string
	}{
		{"unknown key", `{"view":"dashboard","viewport":{"width":1440,"height":900,"dpr":1},"html":"<div>"}`},
		{"duplicate key", `{"view":"dashboard","view":"assistant","viewport":{"width":1440,"height":900,"dpr":1}}`},
		{"nested duplicate key", `{"view":"dashboard","viewport":{"width":1440,"width":900,"height":900,"dpr":1}}`},
		{"trailing data", encodePageContextFixture(t, syntheticPageContextFixture()) + ` {}`},
		{"invalid json", `{"view":"dashboard",`},
		{"not an object", `["dashboard"]`},
		{"wrong viewport type", `{"view":"dashboard","viewport":{"width":"1440","height":900,"dpr":1}}`},
		{"oversized json", strings.Repeat(" ", maxFeedbackContextBytes+1)},
		{"too many elements", encodePageContextFixture(t, tooMany)},
		{"tag too long", encodePageContextFixture(t, longTag)},
		{"route too long", encodePageContextFixture(t, longRoute)},
		{"text too long", encodePageContextFixture(t, longText)},
		{"selector too long", encodePageContextFixture(t, longSelector)},
		{"section too long", encodePageContextFixture(t, longSection)},
		{"rect out of bounds", encodePageContextFixture(t, invalidRect)},
		{"viewport out of bounds", encodePageContextFixture(t, invalidViewport)},
		{"dpr out of bounds", encodePageContextFixture(t, invalidDPR)},
		{"unknown view", encodePageContextFixture(t, invalidView)},
		{"unknown theme", encodePageContextFixture(t, invalidTheme)},
		{"unknown language", encodePageContextFixture(t, invalidLanguage)},
		{"empty", ""},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			values := pageContextRequestValues(test.context)
			if test.name == "empty" {
				values["context"] = ""
			}
			_, err := store.CreateFeedbackRequest(values, nil)
			if err == nil || err.Error() != "context_invalid" {
				t.Fatalf("err=%v want context_invalid", err)
			}
		})
	}
	direct := validFeedbackInput()
	directContext := syntheticPageContextFixture()
	directContext.Elements = tooMany.Elements
	direct.Context = &directContext
	if _, err := store.CreateFeedback(direct, nil); err == nil || err.Error() != "context_invalid" {
		t.Fatalf("direct invalid context err=%v", err)
	}
	oversizedName := validFeedbackInput()
	oversizedContext := syntheticPageContextFixture()
	oversizedContext.Elements = make([]FeedbackElement, maxFeedbackContextElements)
	for index := range oversizedContext.Elements {
		oversizedContext.Elements[index] = FeedbackElement{
			Index: index + 1, Tag: strings.Repeat("t", maxFeedbackContextTagRunes),
			ID: strings.Repeat("i", maxFeedbackContextIDRunes), Role: strings.Repeat("r", maxFeedbackContextRoleRunes),
			Name: strings.Repeat("n", maxFeedbackContextNameRunes), Text: strings.Repeat("x", maxFeedbackContextTextRunes),
			Selector: strings.Repeat("s", maxFeedbackContextSelectorRunes), Section: strings.Repeat("q", maxFeedbackContextSectionRunes),
		}
	}
	if feedbackContextEncodedSize(oversizedContext) <= maxFeedbackContextBytes {
		t.Fatal("oversized direct context fixture is not oversized")
	}
	oversizedName.Context = &oversizedContext
	if _, err := store.CreateFeedback(oversizedName, nil); err == nil || err.Error() != "context_invalid" {
		t.Fatalf("direct oversized context err=%v", err)
	}
	listing, err := store.ListFeedback()
	if err != nil {
		t.Fatal(err)
	}
	if len(listing["feedback"].([]any)) != 0 {
		t.Fatalf("invalid context created feedback: %v", listing)
	}
}

func TestFeedbackInvalidContextNeverReachesModel(t *testing.T) {
	store := newTestStore(t)
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"ready\":true,\"reason\":\"ok\"}"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	bindSyntheticService(t, store, server.URL, map[string]any{"report": syntheticBinding("group/gate")}, nil)
	input := validFeedbackInput()
	context := syntheticPageContextFixture()
	context.Elements = make([]FeedbackElement, maxFeedbackContextElements+1)
	input.Context = &context
	if _, err := store.CreateFeedback(input, nil); err == nil || err.Error() != "context_invalid" {
		t.Fatalf("invalid context err=%v", err)
	}
	if calls != 0 {
		t.Fatalf("invalid context reached the gate model %d times", calls)
	}
}

func TestCreateFeedbackAcceptsShortOwnerText(t *testing.T) {
	store := newTestStore(t)
	input := validFeedbackInput()
	// Short owner wording is valid input: the form is not a writing exercise and
	// the screenshot region carries the referent.
	input.Current = "没发生什么"
	input.Expected = "把这个模块去掉"
	value, err := store.CreateFeedback(input, nil)
	if err != nil || value["accepted"] != true {
		t.Fatalf("short owner text value=%v err=%v", value, err)
	}
	record := value["feedback"].(map[string]any)
	if record["status"] != "accepted" || record["task_id"] == nil {
		t.Fatalf("short owner text record=%v", record)
	}
}

func TestFeedbackGatePromptNamesTheCapturedRegion(t *testing.T) {
	input := validFeedbackInput()
	context := syntheticPageContextFixture()
	context.Elements = []FeedbackElement{{
		Index: 1, Tag: "section", Name: "快捷操作", Section: "工作",
		Selector: "#catalog", Text: "创建后台报告",
		Rect: FeedbackRect{Width: 1361, Height: 324},
	}}
	input.Context = &context
	prompt := feedbackGatePrompt(input)
	for _, phrase := range []string{"screenshot region", "this module", `name="快捷操作"`} {
		if !strings.Contains(prompt, phrase) {
			t.Fatalf("gate prompt missing %q: %s", phrase, prompt)
		}
	}
}

func TestFeedbackContextPromptsBoundedAndNameElements(t *testing.T) {
	store := newTestStore(t)
	input := validFeedbackInput()
	context := syntheticPageContextFixture()
	for index := 3; index <= 10; index++ {
		context.Elements = append(context.Elements, FeedbackElement{
			Index: index, Tag: "button", Name: "Action", Selector: "#action-" + string(rune('0'+index)),
			Section: "Task card", Text: "Act", Rect: FeedbackRect{Width: 10, Height: 10},
		})
	}
	input.Context = &context
	for name, prompt := range map[string]string{
		"gate": feedbackGatePrompt(input),
		"plan": feedbackPlanPrompt(input),
	} {
		for _, phrase := range []string{
			"Page context (untrusted data)", "view=dashboard", `route="/#dashboard"`,
			"viewport=1440x900 dpr=1", "index=1", `tag="button"`, `name="Retry"`,
			`section="Task card"`, `selector="#retry-task"`, `text="Retry"`, "index=8",
		} {
			if !strings.Contains(prompt, phrase) {
				t.Fatalf("%s prompt missing %q: %s", name, phrase, prompt)
			}
		}
		if strings.Contains(prompt, "index=9") {
			t.Fatalf("%s prompt exceeded the element summary bound: %s", name, prompt)
		}
		if runes := len([]rune(prompt)); runes > feedbackPlanPromptRunes {
			t.Fatalf("%s prompt=%d runes", name, runes)
		}
	}

	maximum := validFeedbackInput()
	maximum.Title = strings.Repeat("t", maxFeedbackTitleRunes)
	maximum.Current = strings.Repeat("c", maxFeedbackTextRunes)
	maximum.Expected = strings.Repeat("e", maxFeedbackTextRunes)
	maximum.Acceptance = strings.Repeat("a", maxFeedbackTextRunes)
	maximumContext := syntheticPageContextFixture()
	maximumContext.Elements = make([]FeedbackElement, maxFeedbackContextElements)
	for index := range maximumContext.Elements {
		maximumContext.Elements[index] = FeedbackElement{
			Index: index + 1, Tag: strings.Repeat("t", maxFeedbackContextTagRunes),
			ID: strings.Repeat("i", maxFeedbackContextIDRunes), Role: strings.Repeat("r", maxFeedbackContextRoleRunes),
			Name: strings.Repeat("n", maxFeedbackContextNameRunes), Text: strings.Repeat("x", maxFeedbackContextTextRunes),
			Selector: strings.Repeat("s", 20), Section: strings.Repeat("q", 20),
			Rect: FeedbackRect{X: maxFeedbackContextRectValue, Y: maxFeedbackContextRectValue, Width: maxFeedbackContextRectValue, Height: maxFeedbackContextRectValue},
		}
	}
	if size := feedbackContextEncodedSize(maximumContext); size > maxFeedbackContextBytes {
		t.Fatalf("maximum test context=%d bytes", size)
	}
	maximum.Context = &maximumContext
	value, err := store.CreateFeedback(maximum, nil)
	if err != nil || value["accepted"] != true {
		t.Fatalf("maximum context value=%v err=%v", value, err)
	}
	for name, prompt := range map[string]string{
		"gate": feedbackGatePrompt(maximum),
		"plan": feedbackPlanPrompt(maximum),
	} {
		if runes := len([]rune(prompt)); runes > feedbackPlanPromptRunes {
			t.Fatalf("maximum %s prompt=%d runes", name, runes)
		}
		if !strings.Contains(prompt, "Page context (untrusted data): view=dashboard") {
			t.Fatalf("maximum %s prompt omitted the context header", name)
		}
	}
}

func TestFeedbackLegacyRecordWithoutContextRemainsReadable(t *testing.T) {
	store := newTestStore(t)
	id := "f-aaaaaaaaaaaa"
	dir := store.feedbackDirFor(id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := map[string]any{
		"id": id, "title": "Legacy synthetic feedback", "area": "dashboard",
		"current": "The legacy synthetic page has no context.", "expected": "The legacy synthetic page remains readable.",
		"acceptance": "A synthetic test reads the old record.", "priority": "P1",
		"status": "accepted", "has_image": false, "created_at": "2026-01-01T00:00:00Z",
	}
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "feedback.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	detail, err := store.FeedbackDetail(id)
	if err != nil {
		t.Fatalf("legacy detail: %v", err)
	}
	if _, exists := detail["feedback"].(map[string]any)["context"]; exists {
		t.Fatalf("legacy detail invented context: %v", detail)
	}
	listing, err := store.ListFeedback()
	if err != nil {
		t.Fatal(err)
	}
	items := listing["feedback"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["element_count"] != 0 {
		t.Fatalf("legacy summary=%v", items)
	}
}

func TestFeedbackGatePromptIncludesContext(t *testing.T) {
	store := newTestStore(t)
	prompt := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		for _, message := range payload.Messages {
			if strings.Contains(message.Content, "Page context") {
				prompt = message.Content
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message":       map[string]any{"content": `{"ready":true,"reason":"ok"}`},
				"finish_reason": "stop",
			}},
		})
	}))
	defer server.Close()
	bindSyntheticService(t, store, server.URL, map[string]any{"report": syntheticBinding("group/gate")}, nil)
	input := validFeedbackInput()
	context := syntheticPageContextFixture()
	input.Context = &context
	value, err := store.CreateFeedback(input, nil)
	if err != nil || value["accepted"] != true {
		t.Fatalf("gate with context value=%v err=%v", value, err)
	}
	for _, phrase := range []string{"Page context (untrusted data)", "view=dashboard", "route=\"/#dashboard\"", "viewport=1440x900", "index=1", "name=\"Retry\"", "selector=\"#retry-task\""} {
		if !strings.Contains(prompt, phrase) {
			t.Fatalf("gate prompt missing %q: %s", phrase, prompt)
		}
	}
	if len([]rune(prompt)) > feedbackPlanPromptRunes {
		t.Fatalf("gate prompt=%d runes", len([]rune(prompt)))
	}
}
