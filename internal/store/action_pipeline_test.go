package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPipelineTemplateRendering(t *testing.T) {
	store := newTestStore(t)
	plan := actionPlan{ID: "brief", Label: "Brief"}
	task := taskRecord{ID: "t-0123456789ab", Title: "Today", Source: "test", Priority: "P1", Kind: "brief",
		Payload: map[string]any{"focus": "ship", "count": float64(2)}}
	outputs := map[string]string{"ctx": "context body"}
	values := pipelineTemplateValues(plan, task, outputs)
	rendered, err := store.renderPipeline("{{payload.focus}} / {{payload.count}} / {{steps.ctx.output}} / {{task.title}}", values, false)
	if err != nil || rendered != "ship / 2 / context body / Today" {
		t.Fatalf("rendered=%q err=%v", rendered, err)
	}
	if _, err := store.renderPipeline("{{payload.missing}}", values, false); err == nil {
		t.Fatal("missing template value was accepted")
	}

	secretDir := filepath.Join(store.secretsDir, "actions")
	if err := os.MkdirAll(secretDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secretDir, "token"), []byte("s3cr3t\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.renderPipeline("{{secrets.token}}", values, false); err == nil {
		t.Fatal("secret was resolved in a field that forbids it")
	}
	rendered, err = store.renderPipeline("Bearer {{secrets.token}}", values, true)
	if err != nil || rendered != "Bearer s3cr3t" {
		t.Fatalf("secret render=%q err=%v", rendered, err)
	}
}

func TestPipelineScriptStep(t *testing.T) {
	store := newTestStore(t)
	scripts := filepath.Join(store.dataDir, "scripts")
	if err := os.MkdirAll(scripts, 0o700); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(scripts, "hello.sh")
	writeExecutable(t, script, "#!/bin/sh\nprintf 'hello %s' \"$1\"\n")
	definition := pipelineDefinition{
		ID: "hello", Label: "Hello", Priority: "P1",
		Steps: []pipelineStep{{ID: "run", Type: "script", Command: []string{script, "world"}}},
	}
	if err := store.PutAction(definition); err != nil {
		t.Fatalf("put: %v", err)
	}
	plan, err := store.resolveTaskAction("hello")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	result, err := store.runPipeline(plan, taskRecord{ID: "t-0123456789ab", Title: "Hello", Priority: "P1", Source: "test"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result["status"] != "completed" || result["summary"] != "hello world" {
		t.Fatalf("result=%v", result)
	}
	run, err := store.AutomationRun(result["run_id"].(string))
	if err != nil {
		t.Fatalf("run record: %v", err)
	}
	steps := run["steps"].([]any)
	if len(steps) != 1 {
		t.Fatalf("steps=%v", steps)
	}
	step := steps[0].(map[string]any)
	if step["status"] != "completed" || step["output_digest"] == "" {
		t.Fatalf("step=%v", step)
	}
	if _, exposed := step["output"]; exposed {
		t.Fatalf("step persisted raw output: %v", step)
	}
}

func TestPipelineAgentStepAndReport(t *testing.T) {
	store := newTestStore(t)
	bin := t.TempDir()
	writeExecutable(t, filepath.Join(bin, "codex"), "#!/bin/sh\nexit 0\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	launcher := filepath.Join(store.toolRoot, "folio")
	writeExecutable(t, launcher, `#!/bin/sh
out=""
while [ $# -gt 0 ]; do
  case "$1" in
    --report-out) out="$2"; shift 2 ;;
    *) shift ;;
  esac
done
[ -n "$out" ] || exit 2
printf '%s' '{"text":"# Brief\n\nThe focus is shipping.","usage":{"input_tokens":100,"cached_tokens":20,"output_tokens":10,"total_tokens":130,"origin":"codex"}}' > "$out"
`)
	definition := pipelineDefinition{
		ID: "brief", Label: "Brief", Priority: "P1", ReportFrom: "ask",
		Steps: []pipelineStep{{
			ID: "ask", Type: "agent", Runner: "codex",
			Prompt: "Write a brief about {{payload.focus}}",
		}},
	}
	if err := store.PutAction(definition); err != nil {
		t.Fatalf("put: %v", err)
	}
	plan, err := store.resolveTaskAction("brief")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	task := taskRecord{ID: "t-0123456789ab", Title: "Brief", Priority: "P1", Source: "test",
		Payload: map[string]any{"focus": "shipping"}}
	result, err := store.runPipeline(plan, task)
	if err != nil || result["status"] != "completed" {
		t.Fatalf("result=%v err=%v", result, err)
	}
	run, err := store.AutomationRun(result["run_id"].(string))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if run["summary"] != "The focus is shipping." || run["report_file"] == "" {
		t.Fatalf("run=%v", run)
	}
	usage := run["usage"].(map[string]any)
	if usage["total_tokens"] != float64(130) {
		t.Fatalf("usage=%v", usage)
	}
	body, err := os.ReadFile(filepath.Join(store.reportsDir, run["report_file"].(string)))
	if err != nil || !strings.Contains(string(body), "shipping") {
		t.Fatalf("report=%q err=%v", body, err)
	}
}

func TestPipelineTaskStepEnqueuesFollowup(t *testing.T) {
	store := newTestStore(t)
	if err := store.PutAction(pipelineDefinition{
		ID: "follow", Label: "Follow", Priority: "P1",
		Steps: []pipelineStep{{ID: "run", Type: "script", Command: []string{filepath.Join(store.dataDir, "scripts", "does-not-run.sh")}}},
	}); err != nil {
		t.Fatalf("put follow: %v", err)
	}
	if err := store.PutAction(pipelineDefinition{
		ID: "start", Label: "Start", Priority: "P1",
		Steps: []pipelineStep{{
			ID: "next", Type: "task", Action: "follow", Title: "Follow up",
			Payload: map[string]any{"from": "{{payload.source}}"},
		}},
	}); err != nil {
		t.Fatalf("put start: %v", err)
	}
	plan, err := store.resolveTaskAction("start")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	result, err := store.runPipeline(plan, taskRecord{ID: "t-0123456789ab", Title: "Start", Priority: "P1",
		Source: "test", Payload: map[string]any{"source": "pipe"}})
	if err != nil || result["status"] != "completed" {
		t.Fatalf("result=%v err=%v", result, err)
	}
	listing, err := store.ListTasks()
	if err != nil {
		t.Fatalf("tasks: %v", err)
	}
	tasks := listing["tasks"].([]any)
	if len(tasks) != 1 || tasks[0].(map[string]any)["kind"] != "follow" {
		t.Fatalf("tasks=%v", tasks)
	}
}

func TestPipelineTaskRecursionFailsClosed(t *testing.T) {
	store := newTestStore(t)
	if err := store.PutAction(pipelineDefinition{
		ID: "loop", Label: "Loop", Priority: "P1",
		Steps: []pipelineStep{{ID: "again", Type: "task", Action: "loop"}},
	}); err != nil {
		t.Fatalf("put: %v", err)
	}
	plan, err := store.resolveTaskAction("loop")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	result, err := store.runPipeline(plan, taskRecord{ID: "t-0123456789ab", Title: "Loop", Priority: "P1", Source: "test"})
	if err != nil || result["status"] != "failed" || result["reason"] != "task_recursion" {
		t.Fatalf("result=%v err=%v", result, err)
	}
}

type pipelineTestSender struct{ text string }

func (s *pipelineTestSender) SendWeixinText(_ context.Context, text string) error {
	s.text = text
	return nil
}

func TestPipelinePushStep(t *testing.T) {
	store := newTestStore(t)
	sender := &pipelineTestSender{}
	store.SetWeixinSender(sender)
	if _, err := store.SaveWeixinPush(true); err != nil {
		t.Fatalf("enable push: %v", err)
	}
	plan := actionPlan{
		ID: "notify", Label: "Notify",
		Steps: []actionStep{{Type: "push", Pipeline: &pipelineStep{
			ID: "send", Type: "push", Channel: "weixin", Title: "Hello", Body: "Body {{payload.text}}",
		}}},
	}
	result, err := store.runPipeline(plan, taskRecord{ID: "t-0123456789ab", Title: "Notify", Priority: "P1",
		Source: "test", Payload: map[string]any{"text": "world"}})
	if err != nil || result["status"] != "completed" {
		t.Fatalf("result=%v err=%v", result, err)
	}
	if !strings.Contains(sender.text, "world") {
		t.Fatalf("delivered text=%q", sender.text)
	}
}

func TestDispatchDueRunsRuntimeAction(t *testing.T) {
	store := newTestStore(t)
	writeTaskPolicy(t, store, map[string]bool{}, "00:00", "00:00", 0)
	scripts := filepath.Join(store.dataDir, "scripts")
	if err := os.MkdirAll(scripts, 0o700); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(scripts, "task.sh")
	writeExecutable(t, script, "#!/bin/sh\nprintf 'dispatched'\n")
	if err := store.PutAction(pipelineDefinition{
		ID: "runtime", Label: "Runtime", Priority: "P1",
		Steps: []pipelineStep{{ID: "run", Type: "script", Command: []string{script}}},
	}); err != nil {
		t.Fatal(err)
	}
	created, err := store.CreateTask("runtime", "manual", "Runtime task", "P1", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created["task"].(map[string]any)["kind"] != "runtime" {
		t.Fatalf("created=%v", created)
	}
	result, err := store.DispatchDue()
	if err != nil || result["dispatched"] != 1 || result["run_status"] != "completed" {
		t.Fatalf("dispatch=%v err=%v", result, err)
	}
	listing, err := store.ListTasks()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	task := listing["tasks"].([]any)[0].(map[string]any)
	if task["status"] != "done" {
		t.Fatalf("task=%v", task)
	}
	run, err := store.AutomationRun(task["run_id"].(string))
	if err != nil || run["action"] != "runtime" || run["status"] != "completed" {
		t.Fatalf("run=%v err=%v", run, err)
	}
}
