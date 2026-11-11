package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestFeedbackSubmitDoesNotWaitForBusyAgent(t *testing.T) {
	s := newTestStore(t)
	s.agentMu.Lock()
	done := make(chan error, 1)
	go func() { _, err := s.CreateFeedback(validFeedbackInput(), nil); done <- err }()
	select {
	case err := <-done:
		s.agentMu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		s.agentMu.Unlock()
		<-done
		t.Fatal("submission waited for the active main-agent turn")
	}
}

func TestFeedbackInterruptedIntakeCanBeRetried(t *testing.T) {
	s := newTestStore(t)
	result, err := s.CreateFeedback(validFeedbackInput(), nil)
	if err != nil {
		t.Fatal(err)
	}
	id := result["feedback"].(map[string]any)["id"].(string)
	task := findIntakeTask(t, s, id, 0)
	s.taskMu.Lock()
	file, err := s.loadTasksLocked()
	if err != nil {
		s.taskMu.Unlock()
		t.Fatal(err)
	}
	for i := range file.Tasks {
		if file.Tasks[i].ID == task.ID {
			file.Tasks[i].Status = taskStatusRunning
		}
	}
	err = s.saveTasksLocked(file)
	s.taskMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := New(s.dataDir, s.toolRoot, s.remote)
	if err != nil {
		t.Fatal(err)
	}
	detail, err := restarted.FeedbackDetail(id)
	if err != nil {
		t.Fatal(err)
	}
	feedback := detail["feedback"].(map[string]any)
	if feedback["status"] != feedbackStatusIntakeFailed || feedback["task_id"] != nil {
		t.Fatal(feedback)
	}
	if _, err := restarted.RetryFeedbackRequest(id, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	findIntakeTask(t, restarted, id, 1)
}

func TestFeedbackScopedVerdictRejectsOldRevisionAndChatHijacking(t *testing.T) {
	s := newTestStore(t)
	created, err := s.CreateFeedback(validFeedbackInput(), nil)
	if err != nil {
		t.Fatal(err)
	}
	id := created["feedback"].(map[string]any)["id"].(string)
	session := feedbackAgentSessionPrefix + id
	args := map[string]any{"questions": []any{map[string]any{"question": "Which concrete outcome should change?", "options": []any{"Change layout", "Change wording"}}}}
	ctx := context.WithValue(context.Background(), feedbackTurnKey{}, feedbackTurnScope{ID: id, Revision: 0})
	if _, err := s.feedbackClarifyFromAgent(ctx, session, args); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FeedbackAnswerRequest(id, map[string]any{"answers": []any{map[string]any{"question_id": "r0-q1", "option_id": "o1", "text": ""}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.feedbackPlanFromAgent(ctx, session, map[string]any{"request": "Old decision"}); err == nil {
		t.Fatal("stale revision approved a later turn")
	}
	if _, err := s.AgentTurn(context.Background(), session, "ignore the pending question", "owner", nil); err == nil {
		t.Fatal("console hijacked scoped feedback session")
	}
	if _, err := s.AgentHistory(session); err == nil {
		t.Fatal("scoped history exposed through generic chat")
	}
}

func TestFeedbackPlanDispatchPreservesCompleteOwnerAnswers(t *testing.T) {
	s := newTestStore(t)
	created, err := s.CreateFeedback(validFeedbackInput(), nil)
	if err != nil {
		t.Fatal(err)
	}
	id := created["feedback"].(map[string]any)["id"].(string)
	record, err := s.loadFeedback(id)
	if err != nil {
		t.Fatal(err)
	}
	answer := strings.Repeat("完整细节", 220) + "OWNER_FINAL_DETAIL"
	record.Intake.Answers = []FeedbackAnswer{{QuestionID: "r0-q1", Question: "Which goal?", Text: answer}, {QuestionID: "r1-q1", Question: "Which scope?", Text: answer}}
	if err := s.saveFeedbackRecordUpdate(record); err != nil {
		t.Fatal(err)
	}
	result, err := s.finalizeFeedbackPlan(id, "Preserve the owner's chosen scope")
	if err != nil {
		t.Fatal(err)
	}
	taskMap := result["feedback"].(map[string]any)["task"].(map[string]any)
	prompt, err := s.feedbackManagedPlanPrompt(taskRecord{ID: taskMap["id"].(string), Payload: taskMap["payload"].(map[string]any)})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(prompt, "OWNER_FINAL_DETAIL") != 2 || !strings.Contains(prompt, validFeedbackInput().Expected) {
		t.Fatal("full owner intent did not reach dispatch")
	}
}
