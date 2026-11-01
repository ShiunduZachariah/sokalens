package readiness

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

type Pinger interface {
	PingContext(context.Context) error
}

type SQLChecker struct {
	NameValue string
	DB        Pinger
	Timeout   time.Duration
}

func (s SQLChecker) Name() string {
	if s.NameValue == "" {
		return "sql"
	}
	return s.NameValue
}

func (s SQLChecker) Check() error {
	if s.DB == nil {
		return errors.New("sql pinger is nil")
	}
	timeout := s.Timeout
	if timeout == 0 {
		timeout = 2 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return s.DB.PingContext(ctx)
}

type RedisChecker struct {
	Address  string
	Password string
	Timeout  time.Duration
}

func (r RedisChecker) Name() string {
	return "redis"
}

func (r RedisChecker) Check() error {
	if r.Address == "" {
		return errors.New("redis address is empty")
	}
	timeout := r.Timeout
	if timeout == 0 {
		timeout = 2 * time.Second
	}

	conn, err := net.DialTimeout("tcp", r.Address, timeout)
	if err != nil {
		return err
	}
	defer conn.Close()

	deadline := time.Now().Add(timeout)
	_ = conn.SetDeadline(deadline)
	if r.Password != "" {
		auth := fmt.Sprintf("*2\r\n$4\r\nAUTH\r\n$%d\r\n%s\r\n", len(r.Password), r.Password)
		if _, err := conn.Write([]byte(auth)); err != nil {
			return err
		}
		line, err := bufio.NewReader(conn).ReadString('\n')
		if err != nil {
			return err
		}
		if strings.TrimSpace(line) != "+OK" {
			return fmt.Errorf("unexpected redis AUTH response: %q", strings.TrimSpace(line))
		}
	}
	if _, err := conn.Write([]byte("*1\r\n$4\r\nPING\r\n")); err != nil {
		return err
	}

	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return err
	}
	if strings.TrimSpace(line) != "+PONG" {
		return fmt.Errorf("unexpected redis ping response: %q", strings.TrimSpace(line))
	}
	return nil
}

func RedisPasswordFromURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User == nil {
		return ""
	}
	password, _ := parsed.User.Password()
	return password
}

func RedisAddressFromURL(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if parsed.Scheme != "redis" && parsed.Scheme != "rediss" {
		return "", fmt.Errorf("unsupported redis URL scheme %q", parsed.Scheme)
	}
	host := parsed.Host
	if host == "" {
		return "", errors.New("redis URL host is empty")
	}
	if !strings.Contains(host, ":") {
		host += ":6379"
	}
	return host, nil
}
