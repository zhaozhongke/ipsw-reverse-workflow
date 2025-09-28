package decompile

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// TaskStatus represents the status of a decompilation task.
type TaskStatus string

const (
	StatusPending   TaskStatus = "pending"
	StatusInFlight  TaskStatus = "in_flight"
	StatusCompleted TaskStatus = "completed"
	StatusFailed    TaskStatus = "failed"
)

// Task represents a single decompilation task.
type Task struct {
	ID               int64
	ClassName        string
	SymbolName       string
	AssemblyCode     string
	Status           TaskStatus
	Retries          int
	DecompiledSource sql.NullString
	ErrorMessage     sql.NullString
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// TaskStore manages database operations for decompilation tasks.
type TaskStore struct {
	db *sql.DB
}

// NewTaskStore creates a new TaskStore and initializes the database schema.
func NewTaskStore(dataSourceName string) (*TaskStore, error) {
	db, err := sql.Open("sqlite3", dataSourceName)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	store := &TaskStore{db: db}
	if err := store.initSchema(); err != nil {
		return nil, fmt.Errorf("failed to initialize schema: %w", err)
	}

	return store, nil
}

// initSchema creates the necessary database table if it doesn't exist.
func (s *TaskStore) initSchema() error {
	query := `
    CREATE TABLE IF NOT EXISTS decompilation_tasks (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        class_name TEXT NOT NULL,
        symbol_name TEXT NOT NULL,
        assembly_code TEXT NOT NULL,
        status TEXT NOT NULL CHECK(status IN ('pending', 'in_flight', 'completed', 'failed')),
        retries INTEGER DEFAULT 0,
        decompiled_source TEXT,
        error_message TEXT,
        created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
        updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
        UNIQUE(class_name, symbol_name)
    );`
	_, err := s.db.Exec(query)
	return err
}

// Close closes the database connection.
func (s *TaskStore) Close() error {
	return s.db.Close()
}

// ResetInFlightTasks resets all tasks with "in_flight" status to "pending".
// This is useful for resuming work after a crash.
func (s *TaskStore) ResetInFlightTasks() error {
	query := `UPDATE decompilation_tasks SET status = ? WHERE status = ?`
	_, err := s.db.Exec(query, string(StatusPending), string(StatusInFlight))
	if err != nil {
		return fmt.Errorf("failed to reset in_flight tasks: %w", err)
	}
	return nil
}

// AddTasks adds a batch of tasks to the database, ignoring duplicates.
func (s *TaskStore) AddTasks(ctx context.Context, tasks []*Task) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
        INSERT OR IGNORE INTO decompilation_tasks (class_name, symbol_name, assembly_code, status)
        VALUES (?, ?, ?, ?)
    `)
	if err != nil {
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer stmt.Close()

	for _, task := range tasks {
		_, err := stmt.ExecContext(ctx, task.ClassName, task.SymbolName, task.AssemblyCode, string(StatusPending))
		if err != nil {
			return fmt.Errorf("failed to execute statement for task %s: %w", task.SymbolName, err)
		}
	}

	return tx.Commit()
}

// FetchPendingBatch fetches a batch of pending tasks and marks them as "in_flight".
// This operation is transactional to prevent race conditions.
func (s *TaskStore) FetchPendingBatch(ctx context.Context, batchSize int) ([]*Task, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	query := `
        SELECT id, class_name, symbol_name, assembly_code, status, retries, created_at, updated_at
        FROM decompilation_tasks
        WHERE status = ?
        LIMIT ?`
	rows, err := tx.QueryContext(ctx, query, string(StatusPending), batchSize)
	if err != nil {
		return nil, fmt.Errorf("failed to query pending tasks: %w", err)
	}
	defer rows.Close()

	var tasks []*Task
	var taskIDs []int64
	for rows.Next() {
		var task Task
		if err := rows.Scan(
			&task.ID, &task.ClassName, &task.SymbolName, &task.AssemblyCode,
			&task.Status, &task.Retries, &task.CreatedAt, &task.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan task row: %w", err)
		}
		tasks = append(tasks, &task)
		taskIDs = append(taskIDs, task.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error during rows iteration: %w", err)
	}

	if len(tasks) == 0 {
		return []*Task{}, nil
	}

	// Mark the fetched tasks as "in_flight"
	updateQuery := `UPDATE decompilation_tasks SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE id IN (`
	for i := range taskIDs {
		updateQuery += "?"
		if i < len(taskIDs)-1 {
			updateQuery += ","
		}
	}
	updateQuery += ")"

	args := []interface{}{string(StatusInFlight)}
	for _, id := range taskIDs {
		args = append(args, id)
	}

	_, err = tx.ExecContext(ctx, updateQuery, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to update task statuses to in_flight: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to commit transaction: %w", err)
	}

	return tasks, nil
}

// UpdateTaskSuccess updates a task as successfully completed.
func (s *TaskStore) UpdateTaskSuccess(ctx context.Context, taskID int64, decompiledSource string) error {
	query := `
        UPDATE decompilation_tasks
        SET status = ?, decompiled_source = ?, updated_at = CURRENT_TIMESTAMP
        WHERE id = ?`
	_, err := s.db.ExecContext(ctx, query, string(StatusCompleted), decompiledSource, taskID)
	if err != nil {
		return fmt.Errorf("failed to update task as successful: %w", err)
	}
	return nil
}

// UpdateTaskFailure updates a task as failed.
func (s *TaskStore) UpdateTaskFailure(ctx context.Context, taskID int64, errorMessage string, retryCount int) error {
	query := `
        UPDATE decompilation_tasks
        SET status = ?, error_message = ?, retries = ?, updated_at = CURRENT_TIMESTAMP
        WHERE id = ?`
	_, err := s.db.ExecContext(ctx, query, string(StatusFailed), errorMessage, retryCount, taskID)
	if err != nil {
		return fmt.Errorf("failed to update task as failed: %w", err)
	}
	return nil
}

// GetProgress returns the number of completed tasks and the total number of tasks.
func (s *TaskStore) GetProgress() (completed int64, total int64, err error) {
	err = s.db.QueryRow(`SELECT COUNT(*) FROM decompilation_tasks WHERE status = ?`, string(StatusCompleted)).Scan(&completed)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to count completed tasks: %w", err)
	}

	err = s.db.QueryRow(`SELECT COUNT(*) FROM decompilation_tasks`).Scan(&total)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to count total tasks: %w", err)
	}

	return completed, total, nil
}

// GetAllCompletedTasks retrieves all successfully completed tasks from the database.
func (s *TaskStore) GetAllCompletedTasks() ([]*Task, error) {
	rows, err := s.db.Query(`
		SELECT id, class_name, symbol_name, decompiled_source
		FROM decompilation_tasks
		WHERE status = ? AND decompiled_source IS NOT NULL
		ORDER BY class_name, symbol_name
	`, string(StatusCompleted))
	if err != nil {
		return nil, fmt.Errorf("failed to query completed tasks: %w", err)
	}
	defer rows.Close()

	var tasks []*Task
	for rows.Next() {
		var task Task
		if err := rows.Scan(&task.ID, &task.ClassName, &task.SymbolName, &task.DecompiledSource); err != nil {
			return nil, fmt.Errorf("failed to scan completed task row: %w", err)
		}
		tasks = append(tasks, &task)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error during completed tasks iteration: %w", err)
	}

	return tasks, nil
}