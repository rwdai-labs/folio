package store

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// findIntakeTask returns the queued intake task for one feedback revision.
func findIntakeTask(t *testing.T, s *Store, id string, revision int) taskRecord {
	t.Helper()
	tasks, err := s.ListTasks()
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range tasks["tasks"].([]any) {
		task := item.(map[string]any)
		if task["kind"] != taskKindFeedbackIntake {
			continue
		}
		payload, _ := task["payload"].(map[string]any)
		if payload["feedback_id"] != id || int(payload["revision"].(float64)) != revision {
			continue
		}
		return taskRecord{
			ID: responseString(task, "id"), Kind: taskKindFeedbackIntake,
			Source: taskSourceFeedback, Priority: "P1", Status: taskStatusQueued,
			Title: responseString(task, "title"), Payload: payload,
		}
	}
	t.Fatalf("no intake task for %s revision %d", id, revision)
	return taskRecord{}
}

func TestFeedbackIntakeSendsScreenshotAndFilesPlan(t *testing.T) {
	s := newTestStore(t)
	var sawImage, sawTools, sawRequired bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
			return
		}
		if payload["tool_choice"] != "required" {
			t.Errorf("tool_choice=%v", payload["tool_choice"])
		}
		tools, _ := payload["tools"].([]any)
		names := map[string]bool{}
		for _, item := range tools {
			tool, _ := item.(map[string]any)
			function, _ := tool["function"].(map[string]any)
			names[responseString(function, "name")] = true
		}
		sawTools = names["feedback.plan"] && names["feedback.clarify"]
		sawRequired = true
		messages, _ := payload["messages"].([]any)
		for _, item := range messages {
			message, _ := item.(map[string]any)
			if message["role"] != "user" {
				continue
			}
			parts, ok := message["content"].([]any)
			if !ok {
				continue
			}
			for _, part := range parts {
				content, _ := part.(map[string]any)
				if content["type"] == "image_url" {
					imageURL, _ := content["image_url"].(map[string]any)
					if strings.HasPrefix(responseString(imageURL, "url"), "data:image/jpeg;base64,") {
						sawImage = true
					}
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"group/coding","choices":[{"finish_reason":"tool_calls","message":{"content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"feedback.plan","arguments":"{\"request\":\"Remove the quick actions card\"}"}}]}}]}`))
	}))
	defer server.Close()
	bindSyntheticService(t, s, server.URL+"/v1", map[string]any{"assistant": syntheticBinding("group/coding")}, nil)

	value, err := s.CreateFeedback(validFeedbackInput(), feedbackPNG(t, 320, 200))
	if err != nil {
		t.Fatal(err)
	}
	feedback := value["feedback"].(map[string]any)
	id := feedback["id"].(string)
	if feedback["status"] != feedbackStatusIntakePending || value["task"] != nil {
		t.Fatalf("submission should be pending intake: %v", value)
	}
	result, err := s.runFeedbackIntakeTask(findIntakeTask(t, s, id, 0))
	if err != nil || result["status"] != "reported" {
		t.Fatalf("intake result=%v err=%v", result, err)
	}
	if !sawRequired || !sawTools || !sawImage {
		t.Fatalf("request required=%v tools=%v image=%v", sawRequired, sawTools, sawImage)
	}
	detail, err := s.FeedbackDetail(id)
	if err != nil {
		t.Fatal(err)
	}
	record := detail["feedback"].(map[string]any)
	if record["status"] != feedbackStatusAccepted || record["task_id"] == nil {
		t.Fatalf("detail=%v", record)
	}
	intake := record["intake"].(map[string]any)
	if intake["state"] != "ready" || intake["vision"] != true {
		t.Fatalf("intake=%v", intake)
	}
	task := record["task"].(map[string]any)
	request := task["payload"].(map[string]any)["request"].(string)
	if !strings.Contains(request, "Remove the quick actions card") {
		t.Fatalf("plan prompt missing focus: %s", request)
	}
}

func TestFeedbackIntakeClarifiesAndResumes(t *testing.T) {
	s := newTestStore(t)
	var calls int
	var sawAnswerInInput bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		messages, _ := payload["messages"].([]any)
		var text strings.Builder
		for _, item := range messages {
			message, _ := item.(map[string]any)
			switch content := message["content"].(type) {
			case string:
				text.WriteString(content)
			case []any:
				for _, part := range content {
					value, _ := part.(map[string]any)
					if value["type"] == "text" {
						text.WriteString(responseString(value, "text"))
					}
				}
			}
		}
		if strings.Contains(text.String(), "Keep the launcher") {
			sawAnswerInInput = true
		}
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"tool_calls","message":{"content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"feedback.clarify","arguments":"{\"questions\":[{\"question\":\"Remove the whole card or keep the launcher?\",\"options\":[\"Remove the whole card\",\"Keep the launcher\"],\"allow_text\":false}]}"}}]}}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"tool_calls","message":{"content":"","tool_calls":[{"id":"call_2","type":"function","function":{"name":"feedback.plan","arguments":"{\"request\":\"Remove the quick actions card\"}"}}]}}]}`))
	}))
	defer server.Close()
	bindSyntheticService(t, s, server.URL+"/v1", map[string]any{"assistant": syntheticBinding("group/coding")}, nil)

	value, err := s.CreateFeedback(validFeedbackInput(), feedbackPNG(t, 320, 200))
	if err != nil {
		t.Fatal(err)
	}
	id := value["feedback"].(map[string]any)["id"].(string)
	if _, err := s.runFeedbackIntakeTask(findIntakeTask(t, s, id, 0)); err != nil {
		t.Fatal(err)
	}
	detail, err := s.FeedbackDetail(id)
	if err != nil {
		t.Fatal(err)
	}
	record := detail["feedback"].(map[string]any)
	if record["status"] != feedbackStatusNeedsClarification {
		t.Fatalf("status=%v", record["status"])
	}
	intake := record["intake"].(map[string]any)
	questions := intake["questions"].([]any)
	question := questions[0].(map[string]any)
	options := question["options"].([]any)
	if question["id"] != "q1" || len(options) != 2 {
		t.Fatalf("questions=%v", questions)
	}
	optionID := options[1].(map[string]any)["id"].(string)

	if _, err := s.AnswerFeedback(id, []FeedbackAnswerInput{{QuestionID: "q9", OptionID: optionID}}); err == nil {
		t.Fatal("unknown question accepted")
	}
	value, err = s.AnswerFeedback(id, []FeedbackAnswerInput{{QuestionID: "q1", OptionID: optionID}})
	if err != nil {
		t.Fatal(err)
	}
	record = value["feedback"].(map[string]any)
	if record["status"] != feedbackStatusIntakePending || record["intake"].(map[string]any)["revision"] != 1 {
		t.Fatalf("answer response=%v", record)
	}
	if _, err := s.runFeedbackIntakeTask(findIntakeTask(t, s, id, 1)); err != nil {
		t.Fatal(err)
	}
	if !sawAnswerInInput {
		t.Fatal("second intake did not receive the owner's answer")
	}
	detail, err = s.FeedbackDetail(id)
	if err != nil {
		t.Fatal(err)
	}
	record = detail["feedback"].(map[string]any)
	if record["status"] != feedbackStatusAccepted {
		t.Fatalf("final status=%v", record)
	}
	request := record["task"].(map[string]any)["payload"].(map[string]any)["request"].(string)
	if !strings.Contains(request, "Keep the launcher") {
		t.Fatalf("plan prompt missing answer: %s", request)
	}
}

func TestFeedbackIntakeFallsBackWhenTheModelFails(t *testing.T) {
	s := newTestStore(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "synthetic failure", http.StatusInternalServerError)
	}))
	defer server.Close()
	bindSyntheticService(t, s, server.URL+"/v1", map[string]any{"assistant": syntheticBinding("group/coding")}, nil)

	value, err := s.CreateFeedback(validFeedbackInput(), nil)
	if err != nil {
		t.Fatal(err)
	}
	id := value["feedback"].(map[string]any)["id"].(string)
	result, err := s.runFeedbackIntakeTask(findIntakeTask(t, s, id, 0))
	if err != nil {
		t.Fatal(err)
	}
	if result["status"] != "reported" || result["reason"] != "intake_fallback" {
		t.Fatalf("result=%v", result)
	}
	detail, err := s.FeedbackDetail(id)
	if err != nil {
		t.Fatal(err)
	}
	record := detail["feedback"].(map[string]any)
	if record["status"] != feedbackStatusAccepted || record["task_id"] == nil {
		t.Fatalf("fallback detail=%v", record)
	}
}

func TestFeedbackSubmitWithoutAgentKeepsImmediatePlan(t *testing.T) {
	s := newTestStore(t)
	value, err := s.CreateFeedback(validFeedbackInput(), nil)
	if err != nil {
		t.Fatal(err)
	}
	feedback := value["feedback"].(map[string]any)
	if feedback["status"] != feedbackStatusAccepted || value["task"] == nil {
		t.Fatalf("no-agent submission=%v", value)
	}
}

func TestFeedbackQuestionsAreBoundedAndServerNumbered(t *testing.T) {
	if _, err := parseFeedbackQuestions(map[string]any{"questions": []any{}}); err == nil {
		t.Fatal("empty questions accepted")
	}
	tooMany := []any{}
	for index := 0; index < maxFeedbackQuestions+1; index++ {
		tooMany = append(tooMany, map[string]any{"question": "Synthetic question?", "options": []any{"A", "B"}})
	}
	if _, err := parseFeedbackQuestions(map[string]any{"questions": tooMany}); err == nil {
		t.Fatal("too many questions accepted")
	}
	questions, err := parseFeedbackQuestions(map[string]any{"questions": []any{map[string]any{
		"question": "Which scope should change?", "options": []any{"One", "Two"}, "allow_text": true,
	}}})
	if err != nil || len(questions) != 1 || questions[0].ID != "q1" || questions[0].Options[1].ID != "o2" {
		t.Fatalf("questions=%v err=%v", questions, err)
	}
}
