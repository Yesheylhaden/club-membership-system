package resilience

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	pb "club-membership-system/proto"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ClubClient adds resilience to Membership Service's GetClub calls.
type ClubClient struct {
	client       pb.ClubServiceClient
	timeout      time.Duration
	attempts     int
	backoff      time.Duration
	threshold    int
	openDuration time.Duration

	mu         sync.Mutex
	failures   int
	openUntil  time.Time
	probing    bool
	generation uint64
}

func NewClubClient(
	client pb.ClubServiceClient,
	timeout time.Duration,
	attempts int,
	backoff time.Duration,
	threshold int,
	openDuration time.Duration,
) (*ClubClient, error) {
	if client == nil || timeout <= 0 || attempts < 1 ||
		attempts > 5 || backoff <= 0 || threshold < 1 ||
		openDuration <= 0 {
		return nil, fmt.Errorf("invalid Club client resilience settings")
	}

	return &ClubClient{
		client:       client,
		timeout:      timeout,
		attempts:     attempts,
		backoff:      backoff,
		threshold:    threshold,
		openDuration: openDuration,
	}, nil
}

// permit rejects calls while open and allows one half-open probe.
func (c *ClubClient) permit() (uint64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.openUntil.IsZero() {
		if time.Now().Before(c.openUntil) {
			log.Println("circuit OPEN: Club call rejected")
			return 0, status.Error(
				codes.Unavailable,
				"Club Service circuit is open; try again shortly",
			)
		}

		if c.probing {
			return 0, status.Error(
				codes.Unavailable,
				"Club Service recovery check is in progress",
			)
		}

		c.probing = true
		log.Println("circuit HALF-OPEN: checking Club Service recovery")
	}

	return c.generation, nil
}

func retryable(err error) bool {
	code := status.Code(err)
	return code == codes.Unavailable || code == codes.DeadlineExceeded
}

// record counts failed operations after retries, rather than each attempt.
func (c *ClubClient) record(generation uint64, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Ignore results from calls started before a circuit transition.
	if generation != c.generation {
		return
	}

	// Cancellation by the caller is not evidence of dependency failure.
	if status.Code(err) == codes.Canceled {
		c.probing = false
		return
	}

	if !retryable(err) {
		if !c.openUntil.IsZero() {
			log.Println("circuit CLOSED: Club Service recovered")
			c.generation++
		}

		c.failures = 0
		c.openUntil = time.Time{}
		c.probing = false
		return
	}

	c.failures++

	if c.probing || c.failures >= c.threshold {
		c.openUntil = time.Now().Add(c.openDuration)
		c.probing = false
		c.generation++

		log.Printf(
			"circuit OPEN: Club Service calls blocked for %s",
			c.openDuration,
		)
	}
}

func (c *ClubClient) GetClub(
	ctx context.Context,
	id int64,
) (*pb.Club, error) {
	if id <= 0 {
		return nil, status.Error(
			codes.InvalidArgument, "club ID must be positive",
		)
	}

	if ctx.Err() != nil {
		return nil, status.FromContextError(ctx.Err()).Err()
	}

	generation, err := c.permit()
	if err != nil {
		return nil, err
	}

	result, err := c.callWithRetry(ctx, id)

	// Do not count an expired parent request against Club Service.
	if ctx.Err() != nil {
		c.record(
			generation,
			status.Error(codes.Canceled, "caller request ended"),
		)
		return nil, status.FromContextError(ctx.Err()).Err()
	}

	c.record(generation, err)
	return result, err
}

func (c *ClubClient) callWithRetry(
	ctx context.Context,
	id int64,
) (*pb.Club, error) {
	var lastErr error

	for attempt := 1; attempt <= c.attempts; attempt++ {
		attemptContext, cancel := context.WithTimeout(ctx, c.timeout)

		log.Printf(
			"GetClub id=%d attempt=%d/%d timeout=%s",
			id, attempt, c.attempts, c.timeout,
		)

		result, err := c.client.GetClub(
			attemptContext,
			&pb.GetClubRequest{Id: id},
		)
		cancel()

		if err == nil {
			return result, nil
		}

		if ctx.Err() != nil {
			return nil, status.FromContextError(ctx.Err()).Err()
		}

		lastErr = err
		log.Printf(
			"GetClub attempt=%d failed: code=%s",
			attempt, status.Code(err),
		)

		// Invalid inputs and missing clubs must not be retried.
		if !retryable(err) {
			return nil, err
		}

		if attempt == c.attempts {
			break
		}

		delay := c.backoff * time.Duration(1<<(attempt-1))
		log.Printf("retrying Club call after %s", delay)

		timer := time.NewTimer(delay)

		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, status.FromContextError(ctx.Err()).Err()
		case <-timer.C:
		}
	}

	if status.Code(lastErr) == codes.DeadlineExceeded {
		return nil, status.Error(
			codes.DeadlineExceeded,
			"Club Service timed out after retries; try again later",
		)
	}

	return nil, status.Error(
		codes.Unavailable,
		"Club Service is unavailable after retries; try again later",
	)
}
