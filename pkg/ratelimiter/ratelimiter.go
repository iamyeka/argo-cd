package ratelimiter

import (
	"math"
	"time"

	"golang.org/x/time/rate"
	"k8s.io/client-go/util/workqueue"
)

type AppControllerRateLimiterConfig struct {
	BucketSize      int64
	BucketQPS       float64
	FailureCoolDown time.Duration
	BaseDelay       time.Duration
	MaxDelay        time.Duration
	BackoffFactor   float64
}

func GetDefaultAppRateLimiterConfig() *AppControllerRateLimiterConfig {
	return &AppControllerRateLimiterConfig{
		// global queue rate limit config
		500,
		// when WORKQUEUE_BUCKET_QPS is MaxFloat64 global bucket limiting is disabled(default)
		math.MaxFloat64,
		// individual item rate limit config
		// when WORKQUEUE_FAILURE_COOLDOWN is 0 per item rate limiting is disabled(default)
		0,
		time.Millisecond,
		time.Second,
		1.5,
	}
}

// NewCustomAppControllerRateLimiter is a constructor for the rate limiter for a workqueue used by app controller.  It has
// both overall and per-item rate limiting.  The overall is a token bucket and the per-item is exponential(with auto resets)
func NewCustomAppControllerRateLimiter(cfg *AppControllerRateLimiterConfig) workqueue.RateLimiter {
	return workqueue.NewMaxOfRateLimiter(
		workqueue.NewItemExponentialFailureRateLimiter(cfg.BaseDelay, cfg.MaxDelay),
		&workqueue.BucketRateLimiter{Limiter: rate.NewLimiter(rate.Limit(cfg.BucketQPS), int(cfg.BucketSize))},
	)
}
