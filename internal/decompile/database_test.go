package decompile

import (
	"context"
	"os"
	"sync"
	"testing"
)

func setupTestDB(t *testing.T) *TaskStore {
	// Use a temporary file-based database to ensure connections are shared in concurrent tests.
	tmpfile, err := os.CreateTemp("", "test_odin_*.db")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	t.Cleanup(func() { os.Remove(tmpfile.Name()) })

	// The `?_Cach=shared` is important for concurrent access.
	store, err := NewTaskStore(tmpfile.Name() + "?_cache=shared")
	if err != nil {
		t.Fatalf("failed to create test database: %v", err)
	}
	return store
}

func TestFetchPendingBatch_Transactional(t *testing.T) {
	store := setupTestDB(t)
	defer store.Close()

	// Add some test tasks
	tasks := []*Task{
		{ClassName: "Test", SymbolName: "method1", AssemblyCode: "..."},
		{ClassName: "Test", SymbolName: "method2", AssemblyCode: "..."},
		{ClassName: "Test", SymbolName: "method3", AssemblyCode: "..."},
		{ClassName: "Test", SymbolName: "method4", AssemblyCode: "..."},
	}
	if err := store.AddTasks(context.Background(), tasks); err != nil {
		t.Fatalf("failed to add tasks: %v", err)
	}

	var wg sync.WaitGroup
	fetchedIDs := make(chan int64, 4)
	numGoroutines := 2
	batchSize := 2

	wg.Add(numGoroutines)
	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer wg.Done()
			fetched, err := store.FetchPendingBatch(context.Background(), batchSize)
			if err != nil {
				t.Errorf("fetch pending batch failed: %v", err)
				return
			}
			for _, task := range fetched {
				fetchedIDs <- task.ID
			}
		}()
	}

	wg.Wait()
	close(fetchedIDs)

	// Check for duplicates
	seenIDs := make(map[int64]bool)
	for id := range fetchedIDs {
		if seenIDs[id] {
			t.Errorf("duplicate task ID %d fetched, transaction failed", id)
		}
		seenIDs[id] = true
	}

	if len(seenIDs) != 4 {
		t.Errorf("expected to fetch 4 unique tasks, but got %d", len(seenIDs))
	}
}