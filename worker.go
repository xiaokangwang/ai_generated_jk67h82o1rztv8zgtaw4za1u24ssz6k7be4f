package main

import (
	"log"
	"net"
	"sync"
	"time"
)

// WorkerPool manages concurrent traceroute workers
type WorkerPool struct {
	workers  int
	maxHops  int
	timeout  time.Duration
	mode     string
	jobs     chan *Job
	wg       sync.WaitGroup
}

// Job represents a traceroute job
type Job struct {
	IP      net.IP
	Results chan<- *TraceResult
}

// NewWorkerPool creates a new worker pool
func NewWorkerPool(workers, maxHops int, timeout time.Duration, mode string) *WorkerPool {
	return &WorkerPool{
		workers:  workers,
		maxHops:  maxHops,
		timeout:  timeout,
		mode:     mode,
		jobs:     make(chan *Job, workers*2),
	}
}

// Start starts the worker pool
func (p *WorkerPool) Start() {
	for i := 0; i < p.workers; i++ {
		p.wg.Add(1)
		go p.worker(i)
	}
}

// worker is a single worker goroutine
func (p *WorkerPool) worker(id int) {
	defer p.wg.Done()

	jobNum := 0
	for job := range p.jobs {
		jobNum++
		if jobNum <= 3 {
			log.Printf("Worker %d: Starting job %d for %s", id, jobNum, job.IP)
		}

		var result *TraceResult
		var err error

		// Choose traceroute implementation based on mode
		if p.mode == "raw" {
			result, err = Traceroute(job.IP, p.maxHops, p.timeout)
		} else {
			result, err = TracerouteExternal(job.IP, p.maxHops, p.timeout)
		}

		if err != nil {
			if jobNum <= 3 {
				log.Printf("Worker %d: Error tracing %s: %v", id, job.IP, err)
			}
			// Send error result
			result = &TraceResult{
				DestIP:    job.IP.String(),
				Hops:      []Hop{},
				Reached:   false,
				Timestamp: time.Now(),
			}
		}

		if jobNum <= 3 {
			log.Printf("Worker %d: Completed job %d for %s", id, jobNum, job.IP)
		}

		// Send result
		select {
		case job.Results <- result:
		default:
			log.Printf("Worker %d: Failed to send result for %s", id, job.IP)
		}
	}
}

// Submit submits a new job to the pool
func (p *WorkerPool) Submit(ip net.IP, results chan<- *TraceResult) {
	p.jobs <- &Job{
		IP:      ip,
		Results: results,
	}
}

// Wait waits for all jobs to complete
func (p *WorkerPool) Wait() {
	close(p.jobs)
	p.wg.Wait()
}
