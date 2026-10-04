# Axon

[![CLI Tests](https://github.com/miggle711/Axon/actions/workflows/cli-tests.yml/badge.svg)](https://github.com/miggle711/Axon/actions/workflows/cli-tests.yml)
[![Engine & Worker Tests](https://github.com/miggle711/Axon/actions/workflows/engine-worker-tests.yml/badge.svg)](https://github.com/miggle711/Axon/actions/workflows/engine-worker-tests.yml)
[![Queue Tests](https://github.com/miggle711/Axon/actions/workflows/queue-tests.yml/badge.svg)](https://github.com/miggle711/Axon/actions/workflows/queue-tests.yml)
[![Go Version](https://img.shields.io/badge/go-1.25-00ADD8?logo=go)](https://go.dev/)
[![License](https://img.shields.io/badge/license-Apache%202.0-blue)](LICENSE)

A distributed AI agent orchestration framework with a built-from-scratch task queue.

Agents are defined as JSON, not code: a DAG of steps (tool calls, LLM calls,
conditionals, sub-agent calls, and LLM-supervised loops) that the engine drives to
completion by enqueuing work and reacting to results as they come back.

## Architecture

Four parts:

- **queue**: a Redis-backed job queue: enqueue, dequeue, ack/nack, retries with a
  cap, then permanent failure.
- **engine**: the orchestrator. Loads agent definitions, walks the DAG, enqueues
  the next runnable step(s), and advances the run when the queue tells it a step
  finished (or failed for good).
- **worker**: polls the queue, dispatches each job to a tool, an LLM, or a
  supervisor decision, then reports the result back to the engine.
- **cli**: a thin HTTP client for the engine's API (`axon run`, `axon status`).

```mermaid
sequenceDiagram
    participant Client as cli / curl
    participant Engine as engine
    participant Queue as queue
    participant Worker as worker

    Client->>Engine: POST /runs
    Engine->>Queue: enqueue next runnable step(s)
    loop poll
        Worker->>Queue: GET /jobs/next
    end
    Queue-->>Worker: job
    Worker->>Worker: run tool / call Groq
    Worker->>Engine: POST /webhook/complete
    Engine->>Queue: enqueue the step(s) this unblocked
    Client->>Engine: GET /runs/:id
```

The engine never runs a step itself as it only decides what's runnable next. The
worker never knows about the DAG, it just executes one job at a time and reports
back.

## Step types

An agent's `steps` array can mix five kinds of step:

| Type | What it does |
|---|---|
| `tool_call` | Runs a registered tool (`echo`, `tavily_search`) against `input_template`. |
| `llm_call` | Sends `prompt_template` to Groq, records the completion as output. |
| `conditional` | Evaluates `condition` (`==`, `!=`, or `contains`) against prior step output, resolved inline (no worker round trip), branches to `on_true`/`on_false`. |
| `agent_call` | Spawns another agent as a child run, waits for its `output_step`'s result. |
| `supervisor` | Sends `prompt_template` to Groq, which picks one of `options` to run next; loops until the model answers `done` or the iteration cap is hit. |

Every field can reference earlier results via `{{step_id.output}}`, the original
input via `{{user_input}}`, or (for a supervisor) its own loop count via
`{{step_id.iteration}}`.

A minimal agent (`engine/agents/greeter.json`):

```json
{
  "name": "greeter",
  "output_step": "greet",
  "steps": [
    {
      "id": "greet",
      "type": "tool_call",
      "tool": "echo",
      "input_template": "Hello, {{user_input}}!",
      "depends_on": []
    }
  ]
}
```

Every agent JSON file dropped into `engine/agents/` is loaded and validated at
engine startup; a malformed one (dangling step reference, missing required field,
unknown step type) is rejected with every problem it found, not just the first.

### Worked examples

Four real, committed agents, each exercising a step combination that isn't obvious
from the step type table alone.

**Conditional branching** (`engine/agents/greeter.json` style, as a 4-step version
used in tests): a step runs, then a `conditional` compares its output and routes to
one of two steps.

```json
{
  "id": "check",
  "type": "conditional",
  "condition": "{{step_1.output}} == success",
  "on_true": "on_true_step",
  "on_false": "on_false_step",
  "depends_on": ["step_1"]
}
```

The part that's easy to miss: `on_true`/`on_false` only say where to route. The
target steps each still need `check` in their own `depends_on`, or they'd never
become runnable. `engine/agentbuilder`'s `Conditional` function adds that second
edge automatically; writing this by hand means remembering it twice.

**Calling a sub-agent** (`engine/agents/greeter_caller.json`): one agent calling
another as a child run.

```json
{
  "id": "call_greeter",
  "type": "agent_call",
  "agent": "greeter",
  "input_template": "{{user_input}}",
  "depends_on": []
}
```

The sub-agent (`greeter.json` here) needs `output_step` set, naming which of its
own steps becomes the result `{{call_greeter.output}}` resolves to in the caller.
An agent with no `output_step` can still run standalone, it just can't be called
this way.

**A supervisor loop** (`engine/agents/research_agent.json`): an LLM judges whether
results are good enough, looping until it says so.

```json
{
  "id": "judge",
  "type": "supervisor",
  "prompt_template": "...",
  "options": ["search"],
  "first_iteration_step": "search",
  "depends_on": []
}
```

`judge` has no `depends_on` and nothing to judge yet on its first decision, so
`first_iteration_step` forces that first move to `search` structurally instead of
asking the model to get a from-nothing decision right. From the second iteration
on, `judge` makes a real decision on `search`'s real output, until it answers
`done` or hits the iteration cap (`engine.MaxSupervisorIterations`).

**Fan-in** (`engine/agents/multi_source_research_agent.json`): two independent
steps feeding one.

```json
{
  "id": "answer",
  "type": "llm_call",
  "depends_on": ["background_search", "recent_search"]
}
```

`background_search` and `recent_search` both have `depends_on: []`, so neither
waits on the other. The engine enqueues both the moment a run starts, and they run
in parallel; nothing has to be declared "parallel" anywhere. `answer` just waits on
both, the same way any step waits on more than one dependency.

`engine/agents/agent.schema.json` is a JSON Schema for this format - most editors
(VS Code included) pick it up automatically via the `$schema` field already set in
every committed agent file, giving autocomplete and inline errors while you write
one. It's a hand-authoring aid, not the source of truth: `validateAgentDefinition`
at engine startup is what actually enforces the rules.

For a more complex agent, `engine/agentbuilder` is a typed Go API that generates
this same JSON instead of hand-writing it. A step is a `*Step` value referenced by
variable, so a typo'd reference (a dependency, an option, a conditional branch)
becomes a Go compile error instead of a silently wrong string that only surfaces
once the engine loads the file:

```go
convert := ab.Tool("convert", "pandoc_to_markdown", "{{user_input}}")
fallback := ab.Tool("fallback", "echo", "(pandoc returned nothing for this input)")
success := ab.Tool("success", "echo", ab.Output(convert))
checkEmpty := ab.Conditional("check_empty", ab.Output(convert)+" == ", fallback, success, convert)

agent := ab.New("html_to_markdown_safe", checkEmpty, convert, fallback, success).
    WithOutputStep(success)
```

`ab.Conditional` also wires `fallback`/`success` to depend on `check_empty`
automatically. In hand-written JSON that reverse edge has to be added separately
on each target step, and it's easy to forget one.

The engine itself never imports this package; you write a small Go program, run
it once, and commit the JSON it prints, same as any other agent file. See
`engine/agentbuilder/examples/html_to_markdown_safe` for the full, real example.

## Running locally

The easiest way to run all four parts together is docker-compose.

```sh
docker compose up --build
```

This starts Redis, the queue, the engine, and a worker, wired together. The engine
is reachable at `http://localhost:8000`, the queue at `http://localhost:8080`.

Tool calls that need a real API key (`tavily_search`, and any `llm_call`/`supervisor`
step, which use Groq) need `TAVILY_API_KEY`/`GROQ_API_KEY` set for the worker. Copy
`.env.example` to `.env` and fill in real values before starting the stack; without
it the worker still runs, it just warns and rejects jobs that need a key it doesn't
have.

Once it's up, create a run against a built-in agent (see `engine/agents/`):

```sh
curl -s -X POST http://localhost:8000/runs \
  -H "Content-Type: application/json" \
  -d '{"agent_name":"greeter","input":"world"}'
```

or use the CLI:

```sh
cd cli
go run ./cmd run greeter "world"
go run ./cmd status <run_id>
go run ./cmd list
go run ./cmd init --type conditional --name my_agent
```

### Running without Docker

Each service is a normal Go binary if you'd rather run them directly (useful for
iterating on one service without rebuilding an image). Run each command from the
repository root, one per terminal:

```sh
redis-server --port 6379
(cd queue  && go run ./cmd/main.go -mode api -port 8080)
(cd engine && go run ./cmd --agents agents --port 8000 --queue http://localhost:8080 --redis redis://localhost:6379)
(cd worker && go run ./cmd --queue http://localhost:8080 --engine http://localhost:8000)
```

## Tests

Each module has its own tests, run from that module's directory:

```sh
go test ./...
```
