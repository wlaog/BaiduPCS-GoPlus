package reconcile

import (
	"errors"
	"testing"
	"time"
)

func noSleep(time.Duration) {}

func TestRetrySucceedsAfterTransientFailures(t *testing.T) {
	calls := 0
	err, attempts, exhausted := withRetry(DefaultRetryPolicy(), noSleep, nil, func() error {
		calls++
		if calls < 3 {
			return &RemoteError{Op: "列出云端目录", Path: "/x", ErrType: 3, Msg: "网络错误"}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if attempts != 3 {
		t.Errorf("attempts = %d, want 3", attempts)
	}
	if exhausted {
		t.Error("a successful retry must not report exhaustion")
	}
}

func TestRetryStopsImmediatelyOnNonRetryableError(t *testing.T) {
	calls := 0
	err, _, _ := withRetry(DefaultRetryPolicy(), noSleep, nil, func() error {
		calls++
		return &RemoteError{Op: "列出云端目录", Path: "/x", ErrType: 2, Code: 31066, Msg: "文件不存在"}
	})
	if err == nil {
		t.Fatal("expected the error to surface")
	}
	if calls != 1 {
		t.Errorf("a non-retryable error must not be retried, calls = %d", calls)
	}
}

// This is the crux: a throttling response that looks like an auth failure must
// be retried, because aborting a healthy run is as bad as the alternative.
func TestRetryTreatsThrottleAuthCodeAsTransient(t *testing.T) {
	calls := 0
	throttle := &RemoteError{Op: "列出云端目录", Path: "/x", ErrType: 2, Code: -6, Msg: "请重新登录"}
	err, attempts, _ := withRetry(DefaultRetryPolicy(), noSleep, nil, func() error {
		calls++
		if calls <= 2 {
			return throttle
		}
		return nil
	})
	if err != nil {
		t.Fatalf("a transient -6 must be retried and then succeed, got %v", err)
	}
	if attempts != 3 {
		t.Errorf("attempts = %d, want 3", attempts)
	}
}

func TestRetryReportsExhaustionForPersistentAuthFailure(t *testing.T) {
	persistent := &RemoteError{Op: "列出云端目录", Path: "/x", ErrType: 2, Code: -6, Msg: "请重新登录"}
	calls := 0
	err, attempts, exhausted := withRetry(RetryPolicy{MaxAttempts: 3, BaseDelay: time.Millisecond}, noSleep, nil, func() error {
		calls++
		return persistent
	})
	if err == nil {
		t.Fatal("expected an error")
	}
	if !exhausted {
		t.Error("a failure that survived every attempt must report exhaustion")
	}
	if attempts != 3 || calls != 3 {
		t.Errorf("attempts=%d calls=%d, want 3 each", attempts, calls)
	}
	if !IsAuthError(err) {
		t.Error("the error must be recognisable as an auth failure for the scanner to abort")
	}
}

func TestRetryBackoffGrowsAndIsCapped(t *testing.T) {
	var delays []time.Duration
	policy := RetryPolicy{MaxAttempts: 5, BaseDelay: 100 * time.Millisecond, MaxDelay: 300 * time.Millisecond}
	_, _, _ = withRetry(policy, func(d time.Duration) { delays = append(delays, d) }, nil, func() error {
		return &RemoteError{ErrType: 3, Msg: "网络错误"}
	})
	if len(delays) != 4 {
		t.Fatalf("expected 4 backoff sleeps for 5 attempts, got %d (%v)", len(delays), delays)
	}
	want := []time.Duration{100 * time.Millisecond, 200 * time.Millisecond, 300 * time.Millisecond, 300 * time.Millisecond}
	for i, w := range want {
		if delays[i] != w {
			t.Errorf("delay[%d] = %v, want %v", i, delays[i], w)
		}
	}
}

func TestPaceSkipsTheFirstWaitAndEnforcesSpacingAfterwards(t *testing.T) {
	var waits []time.Duration
	fs := &RemoteFS{
		Retry: RetryPolicy{Spacing: time.Hour},
		Sleep: func(d time.Duration) { waits = append(waits, d) },
	}
	fs.pace() // first request: nothing to wait for
	if len(waits) != 0 {
		t.Fatalf("the first request must not wait, got %v", waits)
	}
	fs.pace() // immediately after: must wait out the remaining spacing
	if len(waits) != 1 {
		t.Fatalf("a rapid second request must wait once, got %v", waits)
	}
	if waits[0] <= 0 || waits[0] > time.Hour {
		t.Errorf("wait = %v, want a positive value at most the spacing", waits[0])
	}
}

func TestPaceDisabledWhenSpacingZero(t *testing.T) {
	var waits []time.Duration
	fs := &RemoteFS{Sleep: func(d time.Duration) { waits = append(waits, d) }}
	fs.pace()
	fs.pace()
	if len(waits) != 0 {
		t.Errorf("spacing 0 must disable pacing, got waits %v", waits)
	}
}

func TestIsUnreachableDistinguishesNotFoundFromFailure(t *testing.T) {
	if IsUnreachable(nil) {
		t.Error("nil is not unreachable")
	}
	if IsUnreachable(&NotFoundError{Path: "/x"}) {
		t.Error("a missing path is not unreachable; it is a legitimate UPLOAD case")
	}
	if !IsUnreachable(&RemoteError{Op: "op", Path: "/x", ErrType: 3, Msg: "网络错误"}) {
		t.Error("a network failure is unreachable")
	}
	if !IsUnreachable(errors.New("something unexpected")) {
		t.Error("an unknown error must be treated as unreachable, never as absence")
	}
}
