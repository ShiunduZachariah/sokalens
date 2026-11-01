package provider

import "time"

type RateLimitHeaders struct {
	DailyLimit      int
	DailyRemaining  int
	MinuteLimit     int
	MinuteRemaining int
	Raw             map[string]string
}

type Response struct {
	Provider      string
	Endpoint      string
	StatusCode    int
	Body          []byte
	Latency       time.Duration
	ResponseBytes int
	RateLimit     RateLimitHeaders
}
