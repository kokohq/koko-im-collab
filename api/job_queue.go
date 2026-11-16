package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Job queue — async processing with immediate 202 response
// ─────────────────────────────────────────────────────────────────────────────

const (
	jobStatusQueued     = "queued"
	jobStatusProcessing = "processing"
	jobStatusDone       = "done"
	jobStatusFailed     = "failed"
)

// JobResultData holds the completed payload for a job.
type JobResultData struct {
	Results    interface{} `json:"results,omitempty"`
	TotalCount int         `json:"total_count,omitempty"`
	Page       int         `json:"page,omitempty"`
	PageSize   int         `json:"page_size,omitempty"`
	Error      string      `json:"error,omitempty"`
}

// JobRecord represents a single async request.
type JobRecord struct {
	ID          string         `json:"id"`
	Status      string         `json:"status"`
	Meta        ResponseMeta   `json:"meta"`
	QueuedAt    time.Time      `json:"queued_at"`
	CompletedAt *time.Time     `json:"completed_at,omitempty"`
	Result      *JobResultData `json:"result,omitempty"`
}

type jobWork struct {
	record *JobRecord
	fn     func() JobResultData
}

// JobQueue manages a buffered channel of pending work and an in-memory store of results.
type JobQueue struct {
	mu      sync.RWMutex
	records map[string]*JobRecord
	work    chan *jobWork
}

// NewJobQueue creates a queue with the given worker count and buffer size.
func NewJobQueue(workers, bufSize int) *JobQueue {
	q := &JobQueue{
		records: map[string]*JobRecord{},
		work:    make(chan *jobWork, bufSize),
	}
	for i := 0; i < workers; i++ {
		go q.runWorker()
	}
	return q
}

func (q *JobQueue) runWorker() {
	for w := range q.work {
		q.mu.Lock()
		w.record.Status = jobStatusProcessing
		q.mu.Unlock()

		result := w.fn()

		q.mu.Lock()
		w.record.Status = jobStatusDone
		if result.Error != "" {
			w.record.Status = jobStatusFailed
		}
		now := time.Now().UTC()
		w.record.CompletedAt = &now
		w.record.Result = &result
		q.mu.Unlock()
	}
}

// Enqueue adds a job to the queue and returns the immutable JobRecord snapshot.
func (q *JobQueue) Enqueue(meta ResponseMeta, fn func() JobResultData) *JobRecord {
	id := newJobID()
	rec := &JobRecord{
		ID:       id,
		Status:   jobStatusQueued,
		Meta:     meta,
		QueuedAt: time.Now().UTC(),
	}
	q.mu.Lock()
	q.records[id] = rec
	q.mu.Unlock()

	select {
	case q.work <- &jobWork{record: rec, fn: fn}:
	default:
		// Queue full — mark failed immediately
		q.mu.Lock()
		rec.Status = jobStatusFailed
		now := time.Now().UTC()
		rec.CompletedAt = &now
		rec.Result = &JobResultData{Error: "job queue full — try again later"}
		q.mu.Unlock()
	}
	return rec
}

// Get returns a snapshot of the job record.
func (q *JobQueue) Get(id string) (*JobRecord, bool) {
	q.mu.RLock()
	defer q.mu.RUnlock()
	r, ok := q.records[id]
	if !ok {
		return nil, false
	}
	// Return a shallow copy to avoid race on caller side
	cp := *r
	return &cp, true
}

// Expire removes completed/failed jobs older than ttl to prevent unbounded growth.
func (q *JobQueue) Expire(ttl time.Duration) {
	cutoff := time.Now().Add(-ttl)
	q.mu.Lock()
	defer q.mu.Unlock()
	for id, r := range q.records {
		if r.CompletedAt != nil && r.CompletedAt.Before(cutoff) {
			delete(q.records, id)
		}
	}
}

// StartExpirer launches a background goroutine that prunes old completed jobs every hour.
func (q *JobQueue) StartExpirer(ctx context.Context, ttl time.Duration) {
	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				q.Expire(ttl)
			case <-ctx.Done():
				return
			}
		}
	}()
}

func newJobID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "job_" + hex.EncodeToString(b)
}
