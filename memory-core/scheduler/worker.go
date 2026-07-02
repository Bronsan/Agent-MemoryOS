// Package scheduler implements the async worker queue for memory processing.
// The pipeline: RawInput → EntityExtraction → Embedding → Summary → GraphUpdate → ImportanceScore
package scheduler

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/agent-memoryos/memory-core/config"
)

// Task represents a unit of work for the async pipeline.
type Task struct {
	ID          string            `json:"id"`
	Type        TaskType          `json:"type"`
	Payload     interface{}       `json:"payload"`
	Metadata    map[string]string `json:"metadata"`
	Attempts    int               `json:"attempts"`
	MaxAttempts int               `json:"max_attempts"`
	CreatedAt   time.Time         `json:"created_at"`
}

// TaskType defines the kind of async work.
type TaskType string

const (
	TaskEntityExtraction  TaskType = "entity_extraction"
	TaskEmbeddingGenerate TaskType = "embedding_generate"
	TaskSummaryGenerate   TaskType = "summary_generate"
	TaskGraphUpdate       TaskType = "graph_update"
	TaskImportanceScore   TaskType = "importance_score"
	TaskDecayApply        TaskType = "decay_apply"
	TaskConflictResolve   TaskType = "conflict_resolve"
	TaskMemoryPromote     TaskType = "memory_promote"
	TaskMemoryArchive     TaskType = "memory_archive"
)

// TaskHandler processes a single task.
type TaskHandler func(ctx context.Context, task *Task) error

// WorkerPool manages a pool of async workers for memory processing.
type WorkerPool struct {
	cfg      config.WorkerConfig
	queue    chan *Task
	handlers map[TaskType]TaskHandler
	mu       sync.RWMutex
	wg       sync.WaitGroup
	ctx      context.Context
	cancel   context.CancelFunc
	metrics  *WorkerMetrics
}

// WorkerMetrics tracks worker pool statistics.
type WorkerMetrics struct {
	mu             sync.Mutex
	TasksQueued    int64
	TasksProcessed int64
	TasksFailed    int64
	TasksRetried   int64
}

// NewWorkerPool creates a new worker pool.
func NewWorkerPool(cfg config.WorkerConfig) *WorkerPool {
	ctx, cancel := context.WithCancel(context.Background())
	return &WorkerPool{
		cfg:      cfg,
		queue:    make(chan *Task, cfg.QueueSize),
		handlers: make(map[TaskType]TaskHandler),
		ctx:      ctx,
		cancel:   cancel,
		metrics:  &WorkerMetrics{},
	}
}

// RegisterHandler maps a task type to its handler function.
func (w *WorkerPool) RegisterHandler(taskType TaskType, handler TaskHandler) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.handlers[taskType] = handler
}

// Enqueue adds a task to the processing queue.
// This is non-blocking; if the queue is full, it returns an error.
func (w *WorkerPool) Enqueue(task *Task) error {
	if task.MaxAttempts == 0 {
		task.MaxAttempts = w.cfg.RetryMaxAttempts
	}
	if task.CreatedAt.IsZero() {
		task.CreatedAt = time.Now()
	}

	select {
	case w.queue <- task:
		w.metrics.mu.Lock()
		w.metrics.TasksQueued++
		w.metrics.mu.Unlock()
		return nil
	default:
		return fmt.Errorf("worker queue full (size=%d)", w.cfg.QueueSize)
	}
}

// Start launches the worker goroutines.
func (w *WorkerPool) Start() {
	for i := 0; i < w.cfg.Concurrency; i++ {
		w.wg.Add(1)
		go w.worker(i)
	}
	log.Printf("worker pool started with %d workers", w.cfg.Concurrency)
}

// Stop gracefully shuts down the worker pool.
func (w *WorkerPool) Stop() {
	w.cancel()
	w.wg.Wait()
	log.Println("worker pool stopped")
}

// Metrics returns a snapshot of current metrics.
func (w *WorkerPool) Metrics() WorkerMetrics {
	w.metrics.mu.Lock()
	defer w.metrics.mu.Unlock()
	return *w.metrics
}

func (w *WorkerPool) worker(id int) {
	defer w.wg.Done()
	log.Printf("worker %d started", id)

	for {
		select {
		case <-w.ctx.Done():
			log.Printf("worker %d shutting down", id)
			return
		case task := <-w.queue:
			w.processTask(task)
		}
	}
}

func (w *WorkerPool) processTask(task *Task) {
	w.mu.RLock()
	handler, ok := w.handlers[task.Type]
	w.mu.RUnlock()

	if !ok {
		log.Printf("no handler for task type: %s", task.Type)
		return
	}

	ctx, cancel := context.WithTimeout(w.ctx, w.cfg.TaskTimeout)
	defer cancel()

	err := handler(ctx, task)
	if err != nil {
		task.Attempts++
		log.Printf("task %s failed (attempt %d/%d): %v", task.ID, task.Attempts, task.MaxAttempts, err)

		if task.Attempts < task.MaxAttempts {
			w.metrics.mu.Lock()
			w.metrics.TasksRetried++
			w.metrics.mu.Unlock()

			// Retry with exponential backoff
			backoff := w.cfg.RetryBackoff * time.Duration(1<<uint(task.Attempts-1))
			time.AfterFunc(backoff, func() {
				select {
				case w.queue <- task:
				default:
					log.Printf("retry queue full for task %s", task.ID)
				}
			})
		} else {
			w.metrics.mu.Lock()
			w.metrics.TasksFailed++
			w.metrics.mu.Unlock()
			log.Printf("task %s permanently failed after %d attempts: %v", task.ID, task.MaxAttempts, err)
		}
		return
	}

	w.metrics.mu.Lock()
	w.metrics.TasksProcessed++
	w.metrics.mu.Unlock()
}
