package engine

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	uuid "github.com/google/uuid"
)

type Orchestrator struct {
	store       RunStore
	queueClient *QueueClient
	agents      AgentRegistry
	logger      *slog.Logger

	// runLocks holds one mutex per run ID, serializing
	// OnStepCompleted per run. Process-local only.
	runLocksMu sync.Mutex
	runLocks   map[string]*sync.Mutex
}

func NewOrchestrator(store RunStore, queueClient *QueueClient, agents AgentRegistry, logger *slog.Logger) *Orchestrator {
	return &Orchestrator{
		store:       store,
		queueClient: queueClient,
		agents:      agents,
		logger:      logger,
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
//
// A supervisor's Options steps are deliberately re-run across loop
// iterations, but that repeat-enqueuing goes through
// enqueueOptionStep directly, not through this function — so this
// check staying strictly one-shot is correct: once an Options step
// finally settles (its supervisor stops), it must not be eligible for
// enqueuing again via the normal resolve loop below.
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
// (PromptTemplate for llm_call and supervisor, InputTemplate otherwise)
// and resolves it via resolveTemplate.
func resolveStepInput(step StepDefinition, run *Run) string {
	template := step.InputTemplate
	if step.Type == StepTypeLLMCall || step.Type == StepTypeSupervisor {
		template = step.PromptTemplate
	}
	return resolveTemplate(template, run)
}

// CreateRun initializes a new Run instance based on the provided AgentDefinition and user input.
// It also sets the ID for the run
func (orchestrator *Orchestrator) CreateRun(ctx context.Context, definition AgentDefinition, userInput string) (*Run, error) {
	return orchestrator.createRun(ctx, definition, userInput, "", "")
}

// CreateRunByName resolves agentName via the orchestrator's
// AgentRegistry and creates a run for it, so a caller (e.g. the CLI)
// doesn't need its own access to agent definitions, only the engine
// needs to know how agents are loaded/stored.
func (orchestrator *Orchestrator) CreateRunByName(ctx context.Context, agentName, userInput string) (*Run, error) {
	if orchestrator.agents == nil {
		return nil, fmt.Errorf("no agent registry configured")
	}
	definition, ok := orchestrator.agents.Get(agentName)
	if !ok {
		return nil, fmt.Errorf("unknown agent %q", agentName)
	}
	return orchestrator.createRun(ctx, definition, userInput, "", "")
}

// createRun is CreateRun's implementation, additionally accepting
// parentRunID/parentStepID so agent_call can spawn a child run that
// knows where to propagate its completion back to.
func (orchestrator *Orchestrator) createRun(ctx context.Context, definition AgentDefinition, userInput string, parentRunID, parentStepID string) (*Run, error) {
	if err := validateAgentDefinition(definition); err != nil {
		orchestrator.logger.Error("invalid agent definition", "agent_name", definition.Name, "error", err)
		return nil, fmt.Errorf("invalid agent definition: %w", err)
	}

	id, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	log := orchestrator.logger.With("run_id", id.String(), "agent_name", definition.Name)
	log.Info("creating run", "parent_run_id", parentRunID, "parent_step_id", parentStepID)

	// Initialize the Run struct with the provided definition and user input
	run := &Run{
		ID:                     id.String(),
		AgentName:              definition.Name,
		UserInput:              userInput,
		Status:                 "in_progress", // Initial status set to in_progress
		Steps:                  definition.Steps,
		StepResults:            make(map[string]string),
		EnqueuedSteps:          make(map[string]string),
		CompletedSteps:         []string{},
		FailedSteps:            []string{},
		SkippedSteps:           []string{},
		ParentRunID:            parentRunID,
		ParentStepID:           parentStepID,
		SupervisorIterations:   make(map[string]int),
		ActiveSupervisorChoice: make(map[string]string),
		CreatedAt:              time.Now(),
		UpdatedAt:              time.Now(),
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
					log.Error("failed to evaluate condition", "step_id", step.ID, "error", err)
					return nil, fmt.Errorf("failed to evaluate condition for step %s: %v", step.ID, err)
				}

				winner, loser := step.OnFalse, step.OnTrue
				if result {
					winner, loser = step.OnTrue, step.OnFalse
				}

				// winner may be "" if that branch intentionally does nothing
				run.StepResults[step.ID] = winner
				run.CompletedSteps = append(run.CompletedSteps, step.ID)
				log.Info("condition evaluated", "step_id", step.ID, "result", result, "winner", winner, "loser", loser)

				if loser != "" {
					run.SkippedSteps = append(run.SkippedSteps, loser)
					run.SkippedSteps = append(run.SkippedSteps, findTransitiveDependents(loser, definition.Steps)...)
				}

				changed = true
				continue
			}

			if step.Type == StepTypeAgentCall {
				childRun, err := orchestrator.spawnChildRun(ctx, step, run)
				if err != nil {
					log.Error("failed to spawn child run", "step_id", step.ID, "error", err)
					return nil, fmt.Errorf("failed to spawn child run for step %s: %v", step.ID, err)
				}
				run.EnqueuedSteps[step.ID] = "run:" + childRun.ID
				log.Info("child run spawned", "step_id", step.ID, "child_run_id", childRun.ID)
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
					Tool:      step.Tool,
					Input:     resolveStepInput(step, run),
				}, 1) // TODO: Default priority set to 1 for initial steps
			if err != nil {
				log.Error("failed to enqueue step", "step_id", step.ID, "step_type", step.Type, "error", err)
				return nil, fmt.Errorf("failed to enqueue step %s: %v", step.ID, err)
			}
			run.EnqueuedSteps[step.ID] = jobID
			log.Info("step enqueued", "step_id", step.ID, "step_type", step.Type, "job_id", jobID)
			changed = true
		}
		if !changed {
			break
		}
	}

	// save the run to the store
	if err := orchestrator.store.SaveRun(ctx, run); err != nil {
		log.Error("failed to save run", "error", err)
		return nil, fmt.Errorf("failed to save run: %v", err)
	}

	return run, nil
}

// spawnChildRun resolves step's Agent name via the registry and starts
// a child run for it, with parent.ID/step.ID as the child's
// ParentRunID/ParentStepID so its completion can propagate back.
func (orchestrator *Orchestrator) spawnChildRun(ctx context.Context, step StepDefinition, parent *Run) (*Run, error) {
	log := orchestrator.logger.With("run_id", parent.ID, "step_id", step.ID)

	if orchestrator.agents == nil {
		log.Error("agent_call failed: no agent registry configured")
		return nil, fmt.Errorf("no agent registry configured")
	}

	childDef, ok := orchestrator.agents.Get(step.Agent)
	if !ok {
		log.Error("agent_call failed: unknown agent", "agent_name", step.Agent)
		return nil, fmt.Errorf("unknown agent %q", step.Agent)
	}

	if childDef.OutputStep == "" {
		log.Error("agent_call failed: agent has no output_step set", "agent_name", step.Agent)
		return nil, fmt.Errorf("agent %q has no output_step set, required to be called via agent_call", step.Agent)
	}
	hasOutputStep := false
	for _, s := range childDef.Steps {
		if s.ID == childDef.OutputStep {
			hasOutputStep = true
			break
		}
	}
	if !hasOutputStep {
		log.Error("agent_call failed: output_step does not match any step ID", "agent_name", step.Agent, "output_step", childDef.OutputStep)
		return nil, fmt.Errorf("agent %q's output_step %q does not match any step ID", step.Agent, childDef.OutputStep)
	}

	return orchestrator.createRun(ctx, childDef, resolveStepInput(step, parent), parent.ID, step.ID)
}

// GetRun fetches a run's current state by ID.
func (orchestrator *Orchestrator) GetRun(ctx context.Context, runID string) (*Run, error) {
	return orchestrator.store.GetRun(ctx, runID)
}

func (orchestrator *Orchestrator) OnStepCompleted(ctx context.Context, payload WebhookPayload) error {
	log := orchestrator.logger.With("run_id", payload.RunID, "step_id", payload.StepID)
	log.Info("step completed", "output_preview", previewText(payload.Output))

	lock := orchestrator.lockFor(payload.RunID)
	lock.Lock()
	defer lock.Unlock()

	// Retrieve the run from the store
	run, err := orchestrator.store.GetRun(ctx, payload.RunID)
	if err != nil {
		log.Error("failed to retrieve run", "error", err)
		return fmt.Errorf("failed to retrieve run: %v", err)
	}

	// A supervisor step's own completion (its LLM decision came back
	// from the worker) is handled entirely separately: it doesn't
	// record a normal StepResults/CompletedSteps entry itself here,
	// since the loop may continue rather than actually finish.
	if step, ok := findStep(run.Steps, payload.StepID); ok && step.Type == StepTypeSupervisor {
		return orchestrator.handleSupervisorDecision(ctx, run, step, payload.Output)
	}

	// If the completing step is the option a still-looping supervisor
	// most recently chose, this isn't normal DAG progression — it's the
	// signal to re-invoke that supervisor for its next decision. Its
	// output is recorded (so {{step.output}} works for anything the
	// option step's own dependents might reference), but it's
	// deliberately not added to CompletedSteps: that stays reserved for
	// the supervisor step itself, once its loop actually ends, so the
	// run-completion count isn't thrown off by an option step that may
	// run several times.
	for supervisorID, chosen := range run.ActiveSupervisorChoice {
		if chosen != payload.StepID {
			continue
		}
		log.Info("option step completed, re-invoking supervisor", "supervisor_step_id", supervisorID)
		run.StepResults[payload.StepID] = payload.Output
		if err := orchestrator.store.SaveRun(ctx, run); err != nil {
			log.Error("failed to save updated run", "error", err)
			return fmt.Errorf("failed to save updated run: %v", err)
		}
		supervisorStep, ok := findStep(run.Steps, supervisorID)
		if !ok {
			log.Error("active supervisor step not found in run.Steps", "supervisor_step_id", supervisorID)
			return fmt.Errorf("active supervisor step %s not found in run.Steps", supervisorID)
		}
		return orchestrator.enqueueSupervisorDecision(ctx, run, supervisorStep)
	}

	run.StepResults[payload.StepID] = payload.Output
	run.CompletedSteps = append(run.CompletedSteps, payload.StepID)
	return orchestrator.finalizeStepCompletion(ctx, run)
}

// previewText truncates s for log output, so a long tool/LLM output
// doesn't dominate a log line.
func previewText(s string) string {
	const maxLen = 200
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// OnStepFailed marks a step as permanently failed (the worker calls
// this once the queue tells it retries are exhausted, see #37/#47) and
// fails the run outright: Status becomes "failed", a new terminal
// state distinct from "in_progress" - without this, a run with a dead
// step looked identical to one still working, since nothing ever told
// the engine the step was never coming back.
//
// If the failed step is a supervisor's own decision, or the option a
// supervisor is currently waiting on, that supervisor's loop is
// abandoned (its ActiveSupervisorChoice entry is cleared) rather than
// left dangling forever. No attempt is made to give the loop another
// chance - a supervisor-related failure just fails the run like any
// other step failing.
//
// If this run was spawned by an agent_call step, the failure
// propagates to the parent run's agent_call step too, the same way
// finalizeStepCompletion already propagates a successful completion -
// otherwise a failed child would strand its parent run forever, one
// level removed from the exact bug this method exists to fix.
func (orchestrator *Orchestrator) OnStepFailed(ctx context.Context, payload WebhookFailedPayload) error {
	log := orchestrator.logger.With("run_id", payload.RunID, "step_id", payload.StepID)
	log.Error("step permanently failed", "reason", payload.Reason)

	lock := orchestrator.lockFor(payload.RunID)
	lock.Lock()
	defer lock.Unlock()

	run, err := orchestrator.store.GetRun(ctx, payload.RunID)
	if err != nil {
		log.Error("failed to retrieve run", "error", err)
		return fmt.Errorf("failed to retrieve run: %v", err)
	}

	// If the failed step is a supervisor's own decision, or the option
	// a supervisor is currently waiting on, that supervisor's loop
	// can't ever continue - clear it rather than leaving a dangling
	// entry pointing at a step that will never complete.
	delete(run.ActiveSupervisorChoice, payload.StepID)
	for supervisorID, chosen := range run.ActiveSupervisorChoice {
		if chosen == payload.StepID {
			delete(run.ActiveSupervisorChoice, supervisorID)
		}
	}

	run.FailedSteps = append(run.FailedSteps, payload.StepID)
	run.Status = "failed"
	run.UpdatedAt = time.Now()

	if err := orchestrator.store.SaveRun(ctx, run); err != nil {
		log.Error("failed to save updated run", "error", err)
		return fmt.Errorf("failed to save updated run: %v", err)
	}

	if run.ParentRunID != "" {
		log.Info("propagating failure to parent", "parent_run_id", run.ParentRunID, "parent_step_id", run.ParentStepID)
		return orchestrator.OnStepFailed(ctx, WebhookFailedPayload{
			RunID:  run.ParentRunID,
			StepID: run.ParentStepID,
			Reason: fmt.Sprintf("child run %s failed: %s", run.ID, payload.Reason),
		})
	}

	return nil
}

// finalizeStepCompletion runs the shared tail of a step's completion:
// resolving any newly-unblocked steps (or supervisor decisions,
// conditionals, agent_call spawns), recomputing run status, saving, and
// propagating completion to a parent run if this run is a child spawned
// via agent_call. Called both by OnStepCompleted's normal path and by
// handleSupervisorDecision once a supervisor loop actually stops.
func (orchestrator *Orchestrator) finalizeStepCompletion(ctx context.Context, run *Run) error {
	log := orchestrator.logger.With("run_id", run.ID)

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
					log.Error("failed to evaluate condition", "step_id", step.ID, "error", err)
					return fmt.Errorf("failed to evaluate condition for step %s: %v", step.ID, err)
				}

				winner, loser := step.OnFalse, step.OnTrue
				if result {
					winner, loser = step.OnTrue, step.OnFalse
				}

				// winner may be "" if that branch intentionally does nothing
				run.StepResults[step.ID] = winner
				run.CompletedSteps = append(run.CompletedSteps, step.ID)
				log.Info("condition evaluated", "step_id", step.ID, "result", result, "winner", winner, "loser", loser)

				if loser != "" {
					run.SkippedSteps = append(run.SkippedSteps, loser)
					run.SkippedSteps = append(run.SkippedSteps, findTransitiveDependents(loser, run.Steps)...)
				}

				changed = true
				continue
			}

			if step.Type == StepTypeAgentCall {
				childRun, err := orchestrator.spawnChildRun(ctx, step, run)
				if err != nil {
					log.Error("failed to spawn child run", "step_id", step.ID, "error", err)
					return fmt.Errorf("failed to spawn child run for step %s: %v", step.ID, err)
				}
				run.EnqueuedSteps[step.ID] = "run:" + childRun.ID
				log.Info("child run spawned", "step_id", step.ID, "child_run_id", childRun.ID)
				changed = true
				continue
			}

			if step.Type == StepTypeSupervisor {
				if err := orchestrator.enqueueSupervisorDecision(ctx, run, step); err != nil {
					log.Error("failed to enqueue supervisor decision", "step_id", step.ID, "error", err)
					return err
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
					Tool:      step.Tool,
					Input:     resolveStepInput(step, run),
				}, 1) // TODO: Default priority set to 1 for subsequent steps
			if err != nil {
				log.Error("failed to enqueue step", "step_id", step.ID, "step_type", step.Type, "error", err)
				return fmt.Errorf("failed to enqueue step %s: %v", step.ID, err)
			}
			run.EnqueuedSteps[step.ID] = jobID
			log.Info("step enqueued", "step_id", step.ID, "step_type", step.Type, "job_id", jobID)
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
		log.Error("failed to save updated run", "error", err)
		return fmt.Errorf("failed to save updated run: %v", err)
	}

	if run.Status == "completed" {
		log.Info("run completed")
	}

	// If this run was spawned by an agent_call step and has now
	// completed, propagate its designated output step's result back to
	// unblock the parent run's ParentStepID. Uses this run's own
	// AgentDefinition.OutputStep, which was already validated to exist
	// in spawnChildRun before this run was created.
	if run.Status == "completed" && run.ParentRunID != "" {
		agentDef, ok := orchestrator.agents.Get(run.AgentName)
		if !ok {
			log.Error("failed to propagate child run completion: unknown agent", "agent_name", run.AgentName)
			return fmt.Errorf("failed to propagate child run %s completion: unknown agent %q", run.ID, run.AgentName)
		}
		log.Info("propagating child run completion to parent", "parent_run_id", run.ParentRunID, "parent_step_id", run.ParentStepID)
		return orchestrator.OnStepCompleted(ctx, WebhookPayload{
			RunID:  run.ParentRunID,
			StepID: run.ParentStepID,
			Output: run.StepResults[agentDef.OutputStep],
		})
	}

	return nil
}

// findStep returns the step with the given ID from steps, if present.
func findStep(steps []StepDefinition, stepID string) (StepDefinition, bool) {
	for _, step := range steps {
		if step.ID == stepID {
			return step, true
		}
	}
	return StepDefinition{}, false
}

// enqueueSupervisorDecision enqueues a queue job for supervisorStep,
// reusing the same llm_call-shaped execution path in the worker (its
// PromptTemplate is resolved and sent as Input). The worker's response
// comes back through the normal webhook path, where OnStepCompleted
// recognizes it as a supervisor step and routes to
// handleSupervisorDecision instead of normal DAG progression.
func (orchestrator *Orchestrator) enqueueSupervisorDecision(ctx context.Context, run *Run, supervisorStep StepDefinition) error {
	log := orchestrator.logger.With("run_id", run.ID, "step_id", supervisorStep.ID)

	jobID, err := orchestrator.queueClient.Enqueue(
		ctx,
		supervisorStep.Type,
		StepPayload{
			RunID:     run.ID,
			StepID:    supervisorStep.ID,
			AgentName: run.AgentName,
			Tool:      supervisorStep.Tool,
			Options:   supervisorStep.Options,
			Input:     resolveStepInput(supervisorStep, run),
		}, 1)
	if err != nil {
		log.Error("failed to enqueue supervisor decision", "error", err)
		return fmt.Errorf("failed to enqueue supervisor decision for step %s: %v", supervisorStep.ID, err)
	}
	run.EnqueuedSteps[supervisorStep.ID] = jobID
	log.Info("supervisor decision requested", "iteration", run.SupervisorIterations[supervisorStep.ID], "job_id", jobID)
	return orchestrator.store.SaveRun(ctx, run)
}

// handleSupervisorDecision processes a supervisor step's LLM decision
// (decision is the worker's raw output for that step): it must exactly
// match SupervisorDoneSignal or one of supervisorStep.Options.
//
//   - An Options match, with fewer than MaxSupervisorIterations already
//     used, enqueues that option step and records it as the supervisor's
//     active choice, awaiting its completion to loop again.
//   - An Options match at the iteration cap is treated the same as a
//     "done" signal instead of enqueuing a 6th option, using the most
//     recently completed option's output as the result (#26/#16 scoping:
//     the cap is a ceiling on option executions, not decisions).
//   - SupervisorDoneSignal before any option has run is an error: there
//     is no prior option output to use as the supervisor's result, and
//     it most likely means the prompt isn't instructing the model
//     correctly.
func (orchestrator *Orchestrator) handleSupervisorDecision(ctx context.Context, run *Run, supervisorStep StepDefinition, decision string) error {
	log := orchestrator.logger.With("run_id", run.ID, "step_id", supervisorStep.ID)
	decision = strings.TrimSpace(decision)
	log.Info("supervisor decision received", "decision", decision)

	isOption := false
	for _, opt := range supervisorStep.Options {
		if decision == opt {
			isOption = true
			break
		}
	}

	if !isOption && decision != SupervisorDoneSignal {
		log.Error("supervisor decision did not match any valid option", "decision", decision, "options", supervisorStep.Options)
		return fmt.Errorf("supervisor step %s: LLM output %q did not match \"%s\" or any of %v",
			supervisorStep.ID, decision, SupervisorDoneSignal, supervisorStep.Options)
	}

	atCap := run.SupervisorIterations[supervisorStep.ID] >= MaxSupervisorIterations
	if isOption && !atCap {
		run.SupervisorIterations[supervisorStep.ID]++
		run.ActiveSupervisorChoice[supervisorStep.ID] = decision
		if err := orchestrator.store.SaveRun(ctx, run); err != nil {
			log.Error("failed to save updated run", "error", err)
			return fmt.Errorf("failed to save updated run: %v", err)
		}

		optionStep, ok := findStep(run.Steps, decision)
		if !ok {
			log.Error("chosen option is not a step in this run", "decision", decision)
			return fmt.Errorf("supervisor step %s: chosen option %q is not a step in this run", supervisorStep.ID, decision)
		}
		return orchestrator.enqueueOptionStep(ctx, run, optionStep)
	}

	// Stopping: either the LLM said "done", or an Options pick hit the
	// iteration cap and is being force-stopped instead of enqueued.
	if run.SupervisorIterations[supervisorStep.ID] == 0 {
		log.Error("supervisor said done before any option ran")
		return fmt.Errorf("supervisor step %s: got %q before any option ran; the prompt must have the model pick an option first",
			supervisorStep.ID, SupervisorDoneSignal)
	}

	lastChoice := run.ActiveSupervisorChoice[supervisorStep.ID]
	delete(run.ActiveSupervisorChoice, supervisorStep.ID)

	run.StepResults[supervisorStep.ID] = run.StepResults[lastChoice]
	run.CompletedSteps = append(run.CompletedSteps, supervisorStep.ID)
	log.Info("supervisor loop stopped", "reason", supervisorStopReason(atCap, decision), "iterations", run.SupervisorIterations[supervisorStep.ID], "last_choice", lastChoice)
	return orchestrator.finalizeStepCompletion(ctx, run)
}

func supervisorStopReason(atCap bool, decision string) string {
	if atCap {
		return "iteration_cap"
	}
	return decision // "done"
}

// enqueueOptionStep enqueues optionStep the way a normal step is
// enqueued, bypassing the queueClient's job-type dispatch quirks by
// reusing the same shape as the main resolve loops in
// createRun/OnStepCompleted.
func (orchestrator *Orchestrator) enqueueOptionStep(ctx context.Context, run *Run, optionStep StepDefinition) error {
	log := orchestrator.logger.With("run_id", run.ID, "step_id", optionStep.ID)

	if optionStep.Type == StepTypeAgentCall {
		childRun, err := orchestrator.spawnChildRun(ctx, optionStep, run)
		if err != nil {
			log.Error("failed to spawn child run for option step", "error", err)
			return fmt.Errorf("failed to spawn child run for option step %s: %v", optionStep.ID, err)
		}
		run.EnqueuedSteps[optionStep.ID] = "run:" + childRun.ID
		log.Info("option step spawned a child run", "child_run_id", childRun.ID)
		return orchestrator.store.SaveRun(ctx, run)
	}

	jobID, err := orchestrator.queueClient.Enqueue(
		ctx,
		optionStep.Type,
		StepPayload{
			RunID:     run.ID,
			StepID:    optionStep.ID,
			AgentName: run.AgentName,
			Tool:      optionStep.Tool,
			Input:     resolveStepInput(optionStep, run),
		}, 1)
	if err != nil {
		log.Error("failed to enqueue option step", "step_type", optionStep.Type, "error", err)
		return fmt.Errorf("failed to enqueue option step %s: %v", optionStep.ID, err)
	}
	run.EnqueuedSteps[optionStep.ID] = jobID
	log.Info("option step enqueued", "step_type", optionStep.Type, "job_id", jobID)
	return orchestrator.store.SaveRun(ctx, run)
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
	// Find the operator and split into operands *before* resolving template
	// placeholders, so an operator-like substring inside a resolved value
	// (e.g. llm_call output containing "!=" or "contains") can never be
	// mistaken for the condition's actual operator (#30).
	var rawLeft, rawRight string
	var op string
	if idx := strings.Index(condition, "=="); idx != -1 {
		op, rawLeft, rawRight = "==", condition[:idx], condition[idx+len("=="):]
	} else if idx := strings.Index(condition, "!="); idx != -1 {
		op, rawLeft, rawRight = "!=", condition[:idx], condition[idx+len("!="):]
	} else if idx := strings.Index(condition, " contains "); idx != -1 {
		op, rawLeft, rawRight = "contains", condition[:idx], condition[idx+len(" contains "):]
	} else {
		return false, fmt.Errorf("unsupported condition format: %s", condition)
	}

	left := strings.TrimSpace(resolveTemplate(rawLeft, run))
	right := strings.TrimSpace(resolveTemplate(rawRight, run))

	switch op {
	case "==":
		return left == right, nil
	case "!=":
		return left != right, nil
	default: // contains
		return strings.Contains(left, right), nil
	}
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
