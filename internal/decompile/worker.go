package decompile

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

// AIRequest represents the JSON payload sent to the LiteLLM API.
type AIRequest struct {
	Model    string `json:"model"`
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
}

// AIResponse represents the expected JSON structure from the LiteLLM API.
type AIResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

// DecompiledResult represents the inner JSON content within the AI response.
type DecompiledResult struct {
	SymbolName       string `json:"symbol_name"`
	DecompiledSource string `json:"decompiled_source"`
	Success          bool   `json:"success"`
	ErrorMessage     string `json:"error_message"`
}

// DecompileWorker is the main function for a worker goroutine.
// It fetches tasks, sends them to the AI for decompilation, and updates the database.
func DecompileWorker(
	ctx context.Context,
	workerID int,
	store *TaskStore,
	litellmURL string,
	model string,
	batchSize int,
	maxRetries int,
) {
	log.Printf("Worker %d started", workerID)
	defer log.Printf("Worker %d finished", workerID)

	for {
		select {
		case <-ctx.Done():
			log.Printf("Worker %d received shutdown signal", workerID)
			return
		default:
			tasks, err := store.FetchPendingBatch(ctx, batchSize)
			if err != nil {
				log.Printf("Worker %d: error fetching batch: %v", workerID, err)
				time.Sleep(5 * time.Second) // Wait before retrying
				continue
			}

			if len(tasks) == 0 {
				// No more tasks, worker can exit.
				log.Printf("Worker %d: no more tasks to process.", workerID)
				return
			}

			log.Printf("Worker %d: processing batch of %d tasks", workerID, len(tasks))

			prompt, err := formatPrompt(tasks)
			if err != nil {
				log.Printf("Worker %d: failed to format prompt: %v", workerID, err)
				// Mark all tasks in this batch as failed
				for _, task := range tasks {
					_ = store.UpdateTaskFailure(ctx, task.ID, "Failed to format prompt", task.Retries+1)
				}
				continue
			}

			results, err := callLiteLLM(ctx, litellmURL, model, prompt)
			if err != nil {
				log.Printf("Worker %d: AI call failed: %v. Marking batch as failed.", workerID, err)
				for _, task := range tasks {
					if task.Retries < maxRetries {
						_ = store.UpdateTaskFailure(ctx, task.ID, err.Error(), task.Retries+1)
					} else {
						_ = store.UpdateTaskFailure(ctx, task.ID, "Max retries exceeded", task.Retries)
					}
				}
				continue
			}

			// Create a map for quick lookup of tasks by symbol name
			taskMap := make(map[string]*Task)
			for _, task := range tasks {
				taskMap[task.SymbolName] = task
			}

			// Process results and update database
			for _, result := range results {
				task, ok := taskMap[result.SymbolName]
				if !ok {
					log.Printf("Worker %d: received result for unknown symbol: %s", workerID, result.SymbolName)
					continue
				}

				if result.Success {
					err = store.UpdateTaskSuccess(ctx, task.ID, result.DecompiledSource)
					if err != nil {
						log.Printf("Worker %d: failed to update task %d as success: %v", workerID, task.ID, err)
					}
				} else {
					log.Printf("Worker %d: AI failed to decompile symbol %s: %s", workerID, result.SymbolName, result.ErrorMessage)
					err = store.UpdateTaskFailure(ctx, task.ID, result.ErrorMessage, task.Retries) // Not a retryable failure from our side
					if err != nil {
						log.Printf("Worker %d: failed to update task %d as failed: %v", workerID, task.ID, err)
					}
				}
			}
		}
	}
}

// formatPrompt creates the JSON prompt for the AI model from a batch of tasks.
func formatPrompt(tasks []*Task) (string, error) {
	var prompt string
	prompt += "Please decompile the following Objective-C methods. Return a JSON array where each object has 'symbol_name', 'decompiled_source', 'success', and 'error_message' fields.\n\n"

	type Method struct {
		SymbolName   string `json:"symbol_name"`
		AssemblyCode string `json:"assembly_code"`
	}

	methods := make([]Method, len(tasks))
	for i, task := range tasks {
		methods[i] = Method{
			SymbolName:   task.SymbolName,
			AssemblyCode: task.AssemblyCode,
		}
	}

	jsonData, err := json.MarshalIndent(methods, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to marshal tasks to JSON: %w", err)
	}

	prompt += string(jsonData)
	return prompt, nil
}

// callLiteLLM sends a request to the LiteLLM API and returns the parsed response.
func callLiteLLM(ctx context.Context, apiURL, model, prompt string) ([]DecompiledResult, error) {
	requestPayload := AIRequest{
		Model: model,
		Messages: []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}{
			{
				Role:    "user",
				Content: prompt,
			},
		},
	}

	jsonData, err := json.Marshal(requestPayload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", apiURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request to LiteLLM: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("LiteLLM API returned non-200 status: %s, body: %s", resp.Status, string(body))
	}

	var aiResponse AIResponse
	if err := json.NewDecoder(resp.Body).Decode(&aiResponse); err != nil {
		return nil, fmt.Errorf("failed to decode AI response: %w", err)
	}

	if len(aiResponse.Choices) == 0 {
		return nil, fmt.Errorf("no choices returned from AI")
	}

	// The actual content is a JSON string within the response, so it needs to be unmarshalled again.
	var results []DecompiledResult
	if err := json.Unmarshal([]byte(aiResponse.Choices[0].Message.Content), &results); err != nil {
		return nil, fmt.Errorf("failed to unmarshal nested JSON from AI content: %w", err)
	}

	return results, nil
}