package engine

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	uuid "github.com/google/uuid"
)

type Orchestrator struct {
	store       RunStore
	queueClient *QueueClient

	// runLocks holds one mutex per run ID, serializing
	// OnStepCompleted per run. Process-local only.
	runLocksMu sync.Mutex
	runLocks   map[string]*sync.Mutex
}

func NewOrchestrator(store RunStore, queueClient *QueueClient) *Orchestrator {
	return &Orchestrator{
		store:       store,
		queueClient: queueClient,
		runLocks:    make(map[string]*sync.Mutex),
	}
}

// lockFor returns the mutex for runID, creating one on first use.
func (orchestrator *Orchestrator) lockFor(runID string) *sync.Mutex {
	orchestrator.runLocksMu.Lock()
	defer orchestrator.runLocksMu.Unlock()
	if orchestrator.runLocks[runID] == nil {
		orchestrator.runLocks[runID] = &sync.Mutex{}
	}
	return orchestrator.runLocks[runID]
}

// canEnqueueStep reports whether step is ready to run: it has not
// already been enqueued, and every dependency in step.DependsOn has
// a recorded result in run.
func canEnqueueStep(step StepDefinition, run *Run) bool {
	// Check if the step has already been enqueued in the run
	if _, exists := run.EnqueuedSteps[step.ID]; exists {
		return false
	}

	// Check if the step has already completed. Conditionals resolve
	// inline and are never added to EnqueuedSteps, so without this
	// check a completed conditional would be re-evaluated forever.
	for _, completedID := range run.CompletedSteps {
		if completedID == step.ID {
			return false
		}
	}

	// Check if the step has been skipped (e.g. the losing branch of a conditional)
	for _, skippedID := range run.SkippedSteps {
		if skippedID == step.ID {
			return false
		}
	}

	// Check if all dependencies of the step are completed
	for _, depID := range step.DependsOn {
		if _, completed := run.StepResults[depID]; !completed {
			return false // Dependency not completed
		}
	}

	return true // All dependencies are satisfied, can enqueue
}

// resolveTemplate substitutes {{user_input}} and {{step_id.output}}
// placeholders in template with values from run.
func resolveTemplate(template string, run *Run) string {
	result := strings.ReplaceAll(template, "{{user_input}}", run.UserInput)
	for stepID, output := range run.StepResults {
		placeholder := fmt.Sprintf("{{%s.output}}", stepID)
		if strings.Contains(result, placeholder) {
			result = strings.ReplaceAll(result, placeholder, output)
		}
	}
	return result
}

// resolveStepInput picks the right template field for step's type
// (PromptTemplate for llm_call, InputTemplate otherwise) and resolves
// it via resolveTemplate.
func resolveStepInput(step StepDefinition, run *Run) string {
	template := step.InputTemplate
	if step.Type == StepTypeLLMCall {
		template = step.PromptTemplate
	}
	return resolveTemplate(template, run)
}

// CreateRun initializes a new Run instance based on the provided AgentDefinition and user input.
// It also sets the ID for the run
func (orchestrator *Orchestrator) CreateRun(ctx context.Context, definition AgentDefinition, userInput string) (*Run, error) {

	id, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}

	// Initialize the Run struct with the provided definition and user input
	run := &Run{
		ID:                   id.String(),
		AgentName:            definition.Name,
		UserInput:            userInput,
		Status:               "in_progress", // Initial status set to in_progress
		Steps:                definition.Steps,
		StepResults:          make(map[string]string),
		EnqueuedSteps:        make(map[string]string),
		CompletedSteps:       []string{},
		FailedSteps:          []string{},
		SkippedSteps:         []string{},
		ParentRunID:          "", // No parent run for the initial run
		ParentStepID:         "", // No parent step for the initial run
		SupervisorIterations: make(map[string]int),
		CreatedAt:            time.Now(),
		UpdatedAt:            time.Now(),
	}

	// Enqueue the initial steps of the workflow that have no
	// dependencies (or, for conditionals, resolve them). Repeats
	// until a full pass makes no further progress, matching
	// OnStepCompleted's loop.
	for {
		changed := false
		for _, step := range definition.Steps {
			if !canEnqueueStep(step, run) {
				continue
			}

			if step.Type == StepTypeConditional {
				result, err := evaluateCondition(step.Condition, run)
				if err != nil {
					return nil, fmt.Errorf("failed to evaluate condition for step %s: %v", step.ID, err)
				}

				winner, loser := step.OnFalse, step.OnTrue
				if result {
					winner, loser = step.OnTrue, step.OnFalse
				}

				// winner may be "" if that branch intentionally does nothing
				run.StepResults[step.ID] = winner
				run.CompletedSteps = append(run.CompletedSteps, step.ID)

				if loser != "" {
					run.SkippedSteps = append(run.SkippedSteps, loser)
					run.SkippedSteps = append(run.SkippedSteps, findTransitiveDependents(loser, definition.Steps)...)
				}

				changed = true
				continue
			}

			// Enqueue the step and update the run's EnqueuedSteps map
			jobID, err := orchestrator.queueClient.Enqueue(
				ctx,
				step.Type,
				StepPayload{
					RunID:     run.ID,
					StepID:    step.ID,
					AgentName: definition.Name,
					Input:     resolveStepInput(step, run),
				}, 1) // TODO: Default priority set to 1 for initial steps
			if err != nil {
				return nil, fmt.Errorf("failed to enqueue step %s: %v", step.ID, err)
			}
			run.EnqueuedSteps[step.ID] = jobID
			changed = true
		}
		if !changed {
			break
		}
	}

	// save the run to the store
	if err := orchestrator.store.SaveRun(ctx, run); err != nil {
		return nil, fmt.Errorf("failed to save run: %v", err)
	}

	return run, nil
}

// GetRun fetches a run's current state by ID.
func (orchestrator *Orchestrator) GetRun(ctx context.Context, runID string) (*Run, error) {
	return orchestrator.store.GetRun(ctx, runID)
}

func (orchestrator *Orchestrator) OnStepCompleted(ctx context.Context, payload WebhookPayload) error {
	lock := orchestrator.lockFor(payload.RunID)
	lock.Lock()
	defer lock.Unlock()

	// Retrieve the run from the store
	run, err := orchestrator.store.GetRun(ctx, payload.RunID)
	if err != nil {
		return fmt.Errorf("failed to retrieve run: %v", err)
	}

	// Update the run's StepResults with the output of the completed step
	run.StepResults[payload.StepID] = payload.Output
	run.CompletedSteps = append(run.CompletedSteps, payload.StepID)

	// Check if any dependent steps can now be enqueued or, for
	// conditionals, resolved. Repeats until a full pass makes no
	// further progress, since resolving a conditional can unblock a
	// step earlier in run.Steps within the same pass.
	for {
		changed := false
		for _, step := range run.Steps {
			if !canEnqueueStep(step, run) {
				continue
			}

			if step.Type == StepTypeConditional {
				result, err := evaluateCondition(step.Condition, run)
				if err != nil {
					return fmt.Errorf("failed to evaluate condition for step %s: %v", step.ID, err)
				}

				winner, loser := step.OnFalse, step.OnTrue
				if result {
					winner, loser = step.OnTrue, step.OnFalse
				}

				// winner may be "" if that branch intentionally does nothing
				run.StepResults[step.ID] = winner
				run.CompletedSteps = append(run.CompletedSteps, step.ID)

				if loser != "" {
					run.SkippedSteps = append(run.SkippedSteps, loser)
					run.SkippedSteps = append(run.SkippedSteps, findTransitiveDependents(loser, run.Steps)...)
				}

				changed = true
				continue
			}

			// Enqueue the step and update the run's EnqueuedSteps map
			jobID, err := orchestrator.queueClient.Enqueue(
				ctx,
				step.Type,
				StepPayload{
					RunID:     run.ID,
					StepID:    step.ID,
					AgentName: run.AgentName,
					Input:     resolveStepInput(step, run),
				}, 1) // TODO: Default priority set to 1 for subsequent steps
			if err != nil {
				return fmt.Errorf("failed to enqueue step %s: %v", step.ID, err)
			}
			run.EnqueuedSteps[step.ID] = jobID
			changed = true
		}
		if !changed {
			break
		}
	}

	// Update the run's status based on the completion of steps.
	// SkippedSteps count toward completion since a skipped step will
	// never itself complete.
	if len(run.CompletedSteps)+len(run.SkippedSteps) == len(run.Steps) {
		run.Status = "completed"
	} else {
		run.Status = "in_progress"
	}

	run.UpdatedAt = time.Now()

	// Save the updated run back to the store
	if err := orchestrator.store.SaveRun(ctx, run); err != nil {
		return fmt.Errorf("failed to save updated run: %v", err)
	}

	return nil
}

// evaluateCondition resolves condition's template placeholders and
// evaluates a single ==, !=, or contains comparison, e.g.
// "{{step_1.output}} == success" or "{{step_1.output}} contains success".
// Values are compared as bare, unquoted strings and quotes are treated as literal characters, not stripped.
//
// == and != require an exact match, which is reliable for tool_call
// output but brittle against free-form llm_call output (e.g. an LLM
// saying "Yes, that worked." instead of "success"). contains checks
// for a substring instead. Prefer instructing the llm_call's prompt to
// answer in a fixed vocabulary (e.g. "respond with exactly one word:
// success or failure") over relying on contains, since contains still
// only matches phrasing that happens to include the target word.
func evaluateCondition(condition string, run *Run) (bool, error) {
	resolvedCondition := resolveTemplate(condition, run)

	// split on the first occurrence of "==", "!=", or "contains" to separate the left and right sides of the condition
	var left, right string
	if strings.Contains(resolvedCondition, "==") {
		parts := strings.SplitN(resolvedCondition, "==", 2)
		left = strings.TrimSpace(parts[0])
		right = strings.TrimSpace(parts[1])
		return left == right, nil
	} else if strings.Contains(resolvedCondition, "!=") {
		parts := strings.SplitN(resolvedCondition, "!=", 2)
		left = strings.TrimSpace(parts[0])
		right = strings.TrimSpace(parts[1])
		return left != right, nil
	} else if strings.Contains(resolvedCondition, " contains ") {
		parts := strings.SplitN(resolvedCondition, " contains ", 2)
		left = strings.TrimSpace(parts[0])
		right = strings.TrimSpace(parts[1])
		return strings.Contains(left, right), nil
	}

	return false, fmt.Errorf("unsupported condition format: %s", condition)
}

// findTransitiveDependents returns a list of step IDs that are
// transitively dependent on the given stepID. It repeatedly scans
// steps, growing the skipped set until a full pass finds nothing new
// Currently O(N^2) but N is expected to be small.
func findTransitiveDependents(stepID string, steps []StepDefinition) []string {
	skipped := map[string]bool{stepID: true}
	for {
		changed := false
		for _, step := range steps {
			if skipped[step.ID] {
				continue
			}
			for _, dep := range step.DependsOn {
				if skipped[dep] {
					skipped[step.ID] = true
					changed = true
					break
				}
			}
		}
		if !changed {
			break
		}
	}

	delete(skipped, stepID)
	result := make([]string, 0, len(skipped))
	for id := range skipped {
		result = append(result, id)
	}
	return result
}
