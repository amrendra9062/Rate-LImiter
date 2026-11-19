package main

import (
	"fmt"
	"log"
	"math"
	"net/http"
	"rate-limiter/limiter"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

func main() {
	rdb := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
	// Initialising to 5 reqs/sec, Burst = 10
	rl := limiter.NewRateLimiter(rdb, 5, 10)
	r := gin.Default()

	rateLimitMiddleware := func(c *gin.Context) {
		ip := c.ClientIP()

		var allowed bool
		var remaining, resetTime, retryMs int64
		var err error

		// 5-attempt retry with brief exponential backoff (10ms, 20ms, 40ms, 80ms)
		const maxRetries = 5
		for attempt := 0; attempt < maxRetries; attempt++ {
			allowed, remaining, resetTime, retryMs, err = rl.Allow(ip)
			if err == nil {
				break
			}
			if attempt < maxRetries-1 {
				time.Sleep(time.Duration(10*(1<<attempt)) * time.Millisecond)
			}
		}

		// Fail-open: if Redis is still unreachable, log and allow traffic through
		if err != nil {
			log.Printf("[WARN] Rate limiter failed after %d attempts for IP %s (failing open): %v", maxRetries, ip, err)
			c.Next()
			return
		}

		c.Header("X-RateLimit-Remaining", strconv.FormatInt(remaining, 10))
		c.Header("X-RateLimit-Reset", strconv.FormatInt(resetTime, 10))

		if !allowed {
			// Ensure at least 1ms safety
			if retryMs <= 0 {
				retryMs = 1
			}

			waitSecs := int(math.Ceil(float64(retryMs) / 1000.0))
			c.Header("Retry-After", strconv.Itoa(waitSecs))

			var retryStr string
			if retryMs >= 1000 {
				retryStr = fmt.Sprintf("%.2fs", float64(retryMs)/1000.0)
			} else {
				retryStr = fmt.Sprintf("%dms", retryMs)
			}

			c.JSON(http.StatusTooManyRequests, gin.H{
				"error":       "Rate limit exceeded",
				"retry_after": retryStr,
			})
			c.Abort()
			return
		}

		c.Next()
	}

	r.GET("/ping", rateLimitMiddleware, func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"message": "pong"})
	})

	r.Run(":8080")
}
