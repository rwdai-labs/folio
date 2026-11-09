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
	if task == nil || task["kind"] != taskKindFeedbackPlan || task["source"] != "manual" || task["priority"] != "P1" {
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
		{"current short", func(input *FeedbackInput) { input.Current = "short" }, nil, "current_short"},
		{"current long", func(input *FeedbackInput) { input.Current = strings.Repeat("x", maxFeedbackTextRunes+1) }, nil, "too_long"},
		{"expected short", func(input *FeedbackInput) { input.Expected = "short" }, nil, "expected_short"},
		{"expected long", func(input *FeedbackInput) { input.Expected = strings.Repeat("x", maxFeedbackTextRunes+1) }, nil, "too_long"},
		{"acceptance short", func(input *FeedbackInput) { input.Acceptance = "short" }, nil, "acceptance_short"},
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

func TestFeedbackModelGate(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		status     int
		accepted   bool
		reason     string
		wantReason string
	}{
		{"ready fenced", "```json\n{\"ready\":true,\"reason\":\"ok\"}\n```", http.StatusOK, true, "", ""},
		{"reject", "{\"ready\":false,\"reason\":\"too_broad\"}", http.StatusOK, false, "too_broad", "too_broad"},
		{"ready with reject reason", "{\"ready\":true,\"reason\":\"too_broad\"}", http.StatusOK, false, "ambiguous", "ambiguous"},
		{"unknown reason", "{\"ready\":false,\"reason\":\"maybe\"}", http.StatusOK, false, "ambiguous", "ambiguous"},
		{"extra field", "{\"ready\":true,\"reason\":\"ok\",\"extra\":1}", http.StatusOK, false, "gate_unavailable", "gate_unavailable"},
		{"wrong type", "{\"ready\":\"yes\",\"reason\":\"ok\"}", http.StatusOK, false, "gate_unavailable", "gate_unavailable"},
		{"prose no object", "the request is fine", http.StatusOK, false, "gate_unavailable", "gate_unavailable"},
		{"upstream failure", "", http.StatusInternalServerError, false, "gate_unavailable", "gate_unavailable"},
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
			if value["accepted"] != test.accepted {
				t.Fatalf("accepted=%v want %v value=%v", value["accepted"], test.accepted, value)
			}
			record := value["feedback"].(map[string]any)
			persisted, err := os.ReadFile(filepath.Join(store.feedbackDirFor(record["id"].(string)), "feedback.json"))
			if err != nil {
				t.Fatal(err)
			}
			var stored map[string]any
			if err := json.Unmarshal(persisted, &stored); err != nil {
				t.Fatal(err)
			}
			if test.status == http.StatusOK {
				usage, ok := record["gate_usage"].(map[string]any)
				if !ok || usage["input_tokens"] != int64(70) || usage["cached_tokens"] != int64(30) || usage["total_tokens"] != int64(120) {
					t.Fatalf("gate usage=%v", record["gate_usage"])
				}
				storedUsage, ok := stored["gate_usage"].(map[string]any)
				if !ok || storedUsage["total_tokens"] != float64(120) {
					t.Fatalf("persisted gate usage=%v", stored["gate_usage"])
				}
			} else if record["gate_usage"] != nil || stored["gate_usage"] != nil {
				t.Fatalf("failed call without a response invented usage: %v", record)
			}
			if test.accepted {
				if record["status"] != "accepted" || record["task_id"] == nil {
					t.Fatalf("accepted record=%v", record)
				}
				return
			}
			if record["status"] != "rejected" || record["reason"] != test.wantReason {
				t.Fatalf("rejected record=%v", record)
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
