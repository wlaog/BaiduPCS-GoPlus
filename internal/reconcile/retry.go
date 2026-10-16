package reconcile

import (
	"errors"
	"strings"
	"time"

	"github.com/qjfoidnh/BaiduPCS-Go/baidupcs/pcserror"
)

// RetryPolicy controls how the cloud reader handles the provider's transient
// failures.
//
// This exists because Baidu throttles sustained listing hard, and its throttling
// response is not a clean 429: it surfaces as a transport EOF or as an
// authentication-looking error such as errno -6 ("please log in again"). Both
// are temporary. Treating them as a fatal session expiry would abort a healthy
// run; treating them as "the directory is empty" would be catastrophic. So the
// only safe response is to back off and retry, and to distinguish a persistent
// failure from a transient one by how it behaves across attempts.
type RetryPolicy struct {
	// MaxAttempts is the total number of tries per operation (>= 1).
	MaxAttempts int
	// BaseDelay is the delay before the second attempt; it doubles each time.
	BaseDelay time.Duration
	// MaxDelay caps the backoff.
	MaxDelay time.Duration
	// Spacing is inserted before every request to keep the request rate low
	// enough that throttling is rare in the first place.
	Spacing time.Duration
}

// DefaultRetryPolicy bounds metadata request starts to five per second per
// adapter, with up to five attempts. This is a conservative starting point,
// not a claim about the provider's undocumented account-specific quota.
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{
		MaxAttempts: 5,
		BaseDelay:   700 * time.Millisecond,
		MaxDelay:    8 * time.Second,
		Spacing:     200 * time.Millisecond,
	}
}

func (p RetryPolicy) withDefaults() RetryPolicy {
	if p.MaxAttempts <= 0 {
		p.MaxAttempts = 5
	}
	if p.BaseDelay <= 0 {
		p.BaseDelay = 700 * time.Millisecond
	}
	if p.MaxDelay <= 0 {
		p.MaxDelay = 8 * time.Second
	}
	if p.Spacing < 0 {
		p.Spacing = 0
	}
	return p
}

// retryableRemoteCodes are the codes Baidu returns for throttling, expired
// anti-bot tokens, and other states that a later attempt can clear.
var retryableRemoteCodes = map[int]bool{
	-6:    true, // 请重新登录 — also emitted when a sustained scan is throttled
	-7:    true, // 登录失效
	-9:    true, // 文件不存在 (transient in listings that race with changes)
	-3:    true, // 不存在
	31045: true, // user not exists / 登录过期
	31064: true, // 未登录
	31171: true, // another async job is running
}

// isRetryable reports whether another attempt could plausibly succeed.
func isRetryable(err error) bool {
	if err == nil {
		return false
	}
	if IsNotFound(err) || IsAuthExpired(err) {
		return false
	}
	var stale *changedState
	if errors.As(err, &stale) || errors.Is(err, ErrUsage) {
		return false
	}
	var re *RemoteError
	if errors.As(err, &re) {
		switch re.ErrType {
		case pcserror.ErrTypeNetError, pcserror.ErrTypeJSONParseError:
			return true
		case pcserror.ErrTypeRemoteError:
			if retryableRemoteCodes[re.Code] {
				return true
			}
			// A remote error with no code and no message carries no information
			// that would make a retry pointless.
			return re.Code == 0 && strings.TrimSpace(re.Msg) == ""
		}
		return false
	}
	// Unknown error shapes (including raw transport errors) are retried: the
	// cost of a retry is a delay, while giving up early can abort a run.
	return true
}

// withRetry runs fn until it succeeds, is not retryable, or attempts run out.
// It reports whether the final error persisted through every attempt, which is
// what distinguishes a real session failure from throttling.
//
// throttle, when non-nil, is invoked before each attempt. Passing it here rather
// than pre-spacing every call keeps a healthy run fast: spacing is only paid
// when an attempt actually happens, and the caller decides how to pace.
func withRetry(policy RetryPolicy, sleep func(time.Duration), throttle func(), fn func() error) (err error, attempts int, exhausted bool) {
	policy = policy.withDefaults()
	var last error
	for attempt := 1; attempt <= policy.MaxAttempts; attempt++ {
		if throttle != nil {
			throttle()
		}
		if err = fn(); err == nil {
			return nil, attempt, false
		}
		last = err
		if !isRetryable(err) {
			return err, attempt, false
		}
		if attempt == policy.MaxAttempts {
			break
		}
		delay := policy.BaseDelay << (attempt - 1)
		if delay > policy.MaxDelay {
			delay = policy.MaxDelay
		}
		if sleep != nil {
			sleep(delay)
		}
	}
	return last, policy.MaxAttempts, true
}
