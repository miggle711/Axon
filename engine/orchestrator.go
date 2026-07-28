package engine

import (
	"context"
	"fmt"
	"strings"
	"time"

	uuid "github.com/google/uuid"
)

type Orchestrator struct {
	store       RunStore
	queueClient *QueueClient
}

func NewOrchestrator(store RunStore, queueClient *QueueClient) *Orchestrator {
	return &Orchestrator{
		store:       store,
		queueClient: queueClient,
	}
}

// canEnqueueStep reports whether step is ready to run: it has not
// already been enqueued, and every dependency in step.DependsOn has
// a recorded result in run.
func canEnqueueStep(step StepDefinition, run *Run) bool {
	// Check if the step has already been enqueued in the run
	if _, exists := run.EnqueuedSteps[step.ID]; exists {
		return false
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

	// Enqueue the initial steps of the workflow that have no dependencies
	for _, step := range definition.Steps {
		if canEnqueueStep(step, run) {
			// Enqueue the step and update the run's EnqueuedSteps map
			jobID, err := orchestrator.queueClient.Enqueue(
				ctx,
				step.Type,
				StepPayload{
					RunID:     run.ID,
					StepID:    step.ID,
					AgentName: definition.Name,
					Input:     resolveTemplate(step.InputTemplate, run),
				}, 1) // TODO: Default priority set to 1 for initial steps
			if err != nil {
				return nil, fmt.Errorf("failed to enqueue step %s: %v", step.ID, err)
			}
			run.EnqueuedSteps[step.ID] = jobID
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
	// Retrieve the run from the store
	run, err := orchestrator.store.GetRun(ctx, payload.RunID)
	if err != nil {
		return fmt.Errorf("failed to retrieve run: %v", err)
	}

	// Update the run's StepResults with the output of the completed step
	run.StepResults[payload.StepID] = payload.Output
	run.CompletedSteps = append(run.CompletedSteps, payload.StepID)

	// Check if any dependent steps can now be enqueued
	for _, step := range run.Steps {
		if canEnqueueStep(step, run) {
			// Enqueue the step and update the run's EnqueuedSteps map
			jobID, err := orchestrator.queueClient.Enqueue(
				ctx,
				step.Type,
				StepPayload{
					RunID:     run.ID,
					StepID:    step.ID,
					AgentName: run.AgentName,
					Input:     resolveTemplate(step.InputTemplate, run),
				}, 1) // TODO: Default priority set to 1 for subsequent steps
			if err != nil {
				return fmt.Errorf("failed to enqueue step %s: %v", step.ID, err)
			}
			run.EnqueuedSteps[step.ID] = jobID
		}
	}

	// Update the run's status based on the completion of steps
	if len(run.CompletedSteps) == len(run.Steps) {
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
// evaluates a single == or != comparison, e.g. "{{step_1.output}} == success".
// Values are compared as bare, unquoted strings and quotes are treated as literal characters, not stripped.
func evaluateCondition(condition string, run *Run) (bool, error) {
	resolvedCondition := resolveTemplate(condition, run)

	// split on the first occurrence of "==" or "!=" to separate the left and right sides of the condition
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
