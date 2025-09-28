# Project Odin: Concurrent Decompilation Engine for `ipsw`

## Overview

Project Odin is a high-performance, concurrent decompilation engine integrated into the `ipsw` toolkit. It is designed to decompile large binaries, such as the `dyld_shared_cache`, into human-readable Objective-C source code. Odin leverages a worker pool model for massive parallelism and uses an external AI service (via LiteLLM) for the core decompilation logic.

This engine is built for reliability and resilience, featuring persistent state management with SQLite. This allows for seamless recovery from interruptions, ensuring that long-running decompilation jobs can be resumed without losing progress.

## Features

- **Concurrent Decompilation**: Utilizes a configurable number of goroutines (workers) to decompile multiple methods in parallel, significantly speeding up the process.
- **Batch Processing**: Workers batch multiple methods into a single request to the AI service, maximizing throughput and efficiency.
- **Persistent & Resumable**: All tasks are stored in a local SQLite database. If the process is interrupted (e.g., with `Ctrl+C`), it can be restarted and will automatically resume from where it left off.
- **LiteLLM Integration**: Communicates with a variety of AI models through a unified LiteLLM proxy endpoint, making it model-agnostic.
- **Dynamic Progress Tracking**: A real-time progress bar shows the status of the decompilation job, including completion count and estimated time remaining.
- **Organized Output**: Decompiled methods are automatically organized and saved into `.m` files based on their class name.

## How It Works

1.  **Initialization**: On the first run, the tool scans the target (currently mocked) to identify all Objective-C methods and populates a SQLite database with a "pending" task for each one.
2.  **Task Distribution**: The engine starts a pool of concurrent workers. Each worker requests a batch of "pending" tasks from the database.
3.  **Transactional State**: When a worker receives a batch, it transactionally updates the status of those tasks to "in_flight". This prevents other workers from picking up the same tasks.
4.  **AI Decompilation**: The worker formats the assembly code from the batched tasks into a structured JSON prompt and sends it to the configured LiteLLM endpoint.
5.  **Result Processing**: The worker parses the AI's response, which contains the decompiled source code for each method. It then updates the database, marking tasks as "completed" or "failed".
6.  **Progress & Assembly**: While the workers are running, a progress bar queries the database to show real-time progress. Once all tasks are complete, the engine reads all successful results from the database and assembles them into `.m` files in the specified output directory.

## Setup

### 1. LiteLLM Environment

This tool requires a running LiteLLM instance to act as a proxy to your chosen AI model.

**Installation**:
```bash
pip install litellm
```

**Run LiteLLM**:
You need a `config.yaml` to point LiteLLM to your model provider (e.g., Ollama, OpenAI, Anthropic).

Example `config.yaml` for a local Ollama instance running `codellama`:
```yaml
model_list:
  - model_name: ollama/codellama
    litellm_params:
      model: ollama/codellama
      api_base: http://localhost:11434
router_settings:
  # ...
```

Start the LiteLLM server:
```bash
litellm --config config.yaml
```
By default, LiteLLM will be available at `http://localhost:4000`.

### 2. Build the `ipsw` Tool

Clone and build the project:
```bash
# Assuming you are in the project's root directory
go build -o ipsw ./cmd/ipsw
```

## Usage

The primary command is `decompile-project`.

```bash
./ipsw decompile-project --input <path/to/assembly> --output-dir <path/to/save> [flags]
```

### Command-Line Flags

| Flag             | Short | Description                                                   | Default                                |
| ---------------- | ----- | ------------------------------------------------------------- | -------------------------------------- |
| `--input`        | `-i`  | **(Required)** Input directory containing assembly files.     | `""`                                   |
| `--output-dir`   | `-o`  | Output directory for decompiled source files.                 | `"decompiled"`                         |
| `--concurrency`  | `-c`  | Number of concurrent workers.                                 | `4`                                    |
| `--batch-size`   | `-b`  | Number of tasks to process in a single AI request.            | `10`                                   |
| `--litellm-url`  |       | LiteLLM API endpoint URL.                                     | `"http://localhost:4000/v1/chat/completions"` |
| `--model`        |       | AI model to use for decompilation (must match LiteLLM config).| `"ollama/codellama"`                   |
| `--max-retries`  |       | Maximum number of retries for a failed task.                  | `3`                                    |
| `--db`           |       | Path to the SQLite database file.                             | `"decompile.db"`                       |

### Example

```bash
# Start a decompilation job with 8 workers
./ipsw decompile-project -i ./CMCaptureFramework/ -o ./decompiled_src -c 8

# If interrupted, simply run the same command again to resume
./ipsw decompile-project -i ./CMCaptureFramework/ -o ./decompiled_src -c 8
```