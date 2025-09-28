package decompile

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/vbauerster/mpb/v7"
	"github.com/vbauerster/mpb/v7/decor"
	"ipsw/internal/decompile"
)

var (
	inputDir     string
	outputDir    string
	concurrency  int
	batchSize    int
	litellmURL   string
	model        string
	maxRetries   int
	dbPath       string
)

func init() {
	DecompileCmd.Flags().StringVarP(&inputDir, "input", "i", "", "Input directory containing assembly files")
	DecompileCmd.Flags().StringVarP(&outputDir, "output-dir", "o", "decompiled", "Output directory for decompiled source files")
	DecompileCmd.Flags().IntVarP(&concurrency, "concurrency", "c", 4, "Number of concurrent workers")
	DecompileCmd.Flags().IntVarP(&batchSize, "batch-size", "b", 10, "Number of tasks to process in a batch")
	DecompileCmd.Flags().StringVar(&litellmURL, "litellm-url", "http://localhost:4000/v1/chat/completions", "LiteLLM API endpoint URL")
	DecompileCmd.Flags().StringVar(&model, "model", "ollama/codellama", "AI model to use for decompilation")
	DecompileCmd.Flags().IntVar(&maxRetries, "max-retries", 3, "Maximum number of retries for a failed task")
	DecompileCmd.Flags().StringVar(&dbPath, "db", "decompile.db", "Path to the SQLite database file")

	DecompileCmd.MarkFlagRequired("input")
}

// DecompileCmd represents the decompile-project command
var DecompileCmd = &cobra.Command{
	Use:   "decompile-project",
	Short: "Concurrently decompile a project using an AI model via LiteLLM",
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Printf("Starting Odin Decompilation Engine...\n")
		fmt.Printf("Configuration:\n")
		fmt.Printf("  - Input Directory: %s\n", inputDir)
		fmt.Printf("  - Output Directory: %s\n", outputDir)
		fmt.Printf("  - Concurrency: %d\n", concurrency)
		fmt.Printf("  - Batch Size: %d\n", batchSize)
		fmt.Printf("  - Database Path: %s\n", dbPath)
		fmt.Println("------------------------------------")

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		// Setup signal handling for graceful shutdown
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
		go func() {
			<-sigChan
			fmt.Println("\nReceived shutdown signal. Gracefully stopping workers...")
			cancel()
		}()

		if err := os.MkdirAll(outputDir, 0755); err != nil {
			return fmt.Errorf("failed to create output directory: %w", err)
		}

		store, err := decompile.NewTaskStore(dbPath)
		if err != nil {
			return fmt.Errorf("failed to initialize task store: %w", err)
		}
		defer store.Close()

		// Check if this is the first run
		_, total, err := store.GetProgress()
		if err != nil {
			return fmt.Errorf("failed to get initial progress: %w", err)
		}

		if total == 0 {
			fmt.Println("First run detected. Scanning for tasks...")
			// In a real scenario, we would scan inputDir. Here we use mock data.
			tasks, err := createMockTasks()
			if err != nil {
				return fmt.Errorf("failed to create mock tasks: %w", err)
			}
			if err := store.AddTasks(ctx, tasks); err != nil {
				return fmt.Errorf("failed to add initial tasks: %w", err)
			}
			fmt.Printf("Added %d tasks to the database.\n", len(tasks))
		} else {
			fmt.Println("Resuming previous session. Resetting in-flight tasks...")
			if err := store.ResetInFlightTasks(); err != nil {
				return fmt.Errorf("failed to reset in-flight tasks: %w", err)
			}
		}

		// Start the worker pool
		var wg sync.WaitGroup
		wg.Add(concurrency)

		for i := 0; i < concurrency; i++ {
			go func(workerID int) {
				defer wg.Done()
				// The decompileWorker function now needs to be public to be accessible here
				// I will adjust the worker.go file for that.
				decompile.DecompileWorker(ctx, workerID, store, litellmURL, model, batchSize, maxRetries)
			}(i)
		}

		// Start progress bar
		p := mpb.New(mpb.WithWaitGroup(&wg))
		_, total, err = store.GetProgress()
		if err != nil {
			return fmt.Errorf("failed to get progress for progress bar: %w", err)
		}
		bar := p.New(total,
			mpb.BarStyle().Lbound("[").Filler("=").Tip(">").Padding(" ").Rbound("]"),
			mpb.PrependDecorators(
				decor.Name("Decompiling"),
				decor.CountersKibiByte("%d / %d"),
			),
			mpb.AppendDecorators(
				decor.OnComplete(
					decor.EwmaETA(decor.ET_STYLE_GO, 60), "done",
				),
			),
		)

		// Goroutine to update the progress bar
		go func() {
			for {
				completed, _, _ := store.GetProgress()
				bar.SetCurrent(completed)
				time.Sleep(1 * time.Second)
				if completed >= total {
					break
				}
			}
		}()


		wg.Wait()
		p.Wait()

		fmt.Println("\nAll workers have finished. Assembling final files...")
		if err := assembleFiles(store, outputDir); err != nil {
			return fmt.Errorf("failed to assemble files: %w", err)
		}

		fmt.Println("Decompilation process completed successfully.")
		return nil
	},
}

// createMockTasks simulates scanning the input directory and creating tasks.
// Replace this with actual file scanning logic.
func createMockTasks() ([]*decompile.Task, error) {
	return []*decompile.Task{
		{ClassName: "CMCapture", SymbolName: "-[CMCaptureController startCapture]", AssemblyCode: "asm for startCapture..."},
		{ClassName: "CMCapture", SymbolName: "-[CMCaptureController stopCapture]", AssemblyCode: "asm for stopCapture..."},
		{ClassName: "CMCapture", SymbolName: "-[CMCaptureController setZoom:]", AssemblyCode: "asm for setZoom..."},
		{ClassName: "CMWhatever", SymbolName: "-[CMWhatever doSomething]", AssemblyCode: "asm for doSomething..."},
		{ClassName: "CMWhatever", SymbolName: "-[CMWhatever doSomethingElse]", AssemblyCode: "asm for doSomethingElse..."},
	}, nil
}

// assembleFiles reads all successful tasks from the database and writes them
// into .m files, organized by class name.
func assembleFiles(store *decompile.TaskStore, outputDir string) error {
	tasks, err := store.GetAllCompletedTasks()
	if err != nil {
		return fmt.Errorf("could not fetch completed tasks: %w", err)
	}

	files := make(map[string]*os.File)
	defer func() {
		for _, f := range files {
			f.Close()
		}
	}()

	for _, task := range tasks {
		if !task.DecompiledSource.Valid {
			continue // Skip tasks with no decompiled source
		}

		fileName := fmt.Sprintf("%s.m", task.ClassName)
		filePath := filepath.Join(outputDir, fileName)

		f, ok := files[filePath]
		if !ok {
			f, err = os.OpenFile(filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
			if err != nil {
				return fmt.Errorf("failed to open file %s: %w", filePath, err)
			}
			files[filePath] = f
		}

		header := fmt.Sprintf("\n// Decompiled symbol: %s\n", task.SymbolName)
		if _, err := f.WriteString(header + task.DecompiledSource.String + "\n"); err != nil {
			return fmt.Errorf("failed to write to file %s: %w", filePath, err)
		}
	}

	fmt.Printf("Successfully assembled %d tasks into .m files in %s\n", len(tasks), outputDir)
	return nil
}