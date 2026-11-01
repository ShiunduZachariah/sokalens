package readiness

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

type fakePinger struct {
	err error
}

func (f fakePinger) PingContext(context.Context) error {
	return f.err
}

func TestSQLCheckerPassesWhenPingSucceeds(t *testing.T) {
	checker := SQLChecker{NameValue: "postgres", DB: fakePinger{}, Timeout: time.Second}

	if checker.Name() != "postgres" {
		t.Fatalf("Name = %q, want postgres", checker.Name())
	}
	if err := checker.Check(); err != nil {
		t.Fatalf("Check returned error: %v", err)
	}
}

func TestSQLCheckerFailsWhenPingFails(t *testing.T) {
	checker := SQLChecker{NameValue: "postgres", DB: fakePinger{err: errors.New("down")}, Timeout: time.Second}

	if err := checker.Check(); err == nil {
		t.Fatal("Check returned nil error for failed ping")
	}
}

func TestRedisCheckerSendsPingAndAcceptsPong(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 64)
		_, _ = conn.Read(buf)
		_, _ = conn.Write([]byte("+PONG\r\n"))
	}()

	checker := RedisChecker{Address: listener.Addr().String(), Timeout: time.Second}
	if err := checker.Check(); err != nil {
		t.Fatalf("Check returned error: %v", err)
	}
	<-done
}

func TestRedisAddressFromURLUsesDefaultRedisPort(t *testing.T) {
	addr, err := RedisAddressFromURL("redis://redis:6379/0")
	if err != nil {
		t.Fatalf("RedisAddressFromURL returned error: %v", err)
	}
	if addr != "redis:6379" {
		t.Fatalf("addr = %q, want redis:6379", addr)
	}
}
