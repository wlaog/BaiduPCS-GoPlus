package reconcile

import (
	"errors"
	"fmt"
	"strings"

	"github.com/qjfoidnh/BaiduPCS-Go/baidupcs/pcserror"
)

var (
	// ErrNoClient means no authenticated cloud client was supplied.
	ErrNoClient = errors.New("未提供已登录的云端客户端")
	// ErrUsage wraps caller-input problems; the CLI turns it into a usage error.
	ErrUsage = errors.New("参数错误")
)

// NotFoundError means the requested cloud path does not exist. Callers use
// errors.As to distinguish "absent" from "unreachable", which is the difference
// between a legitimate UPLOAD plan and an abort.
type NotFoundError struct {
	Path string
	// Cause is the underlying upstream error, kept for the operator log.
	Cause error
}

func (e *NotFoundError) Error() string {
	return fmt.Sprintf("云端路径不存在: %s", e.Path)
}

func (e *NotFoundError) Unwrap() error { return e.Cause }

// RemoteError wraps an upstream pcserror.Error together with the operation and
// path that produced it, so a report line says what was attempted where.
type RemoteError struct {
	Op   string
	Path string
	// ErrType mirrors pcserror.ErrType so retry logic can branch on it.
	ErrType pcserror.ErrType
	// Code is the remote error code (0 when not applicable).
	Code int
	// Msg is the remote error message.
	Msg string
	// Cause is the original error, unwrapped for errors.Is/As.
	Cause error
}

func (e *RemoteError) Error() string {
	base := fmt.Sprintf("%s 失败: %s", e.Op, e.Path)
	switch {
	case e.Code != 0:
		return fmt.Sprintf("%s (%s, 错误码 %d)", base, e.Msg, e.Code)
	case e.Msg != "":
		return fmt.Sprintf("%s (%s)", base, e.Msg)
	default:
		return base
	}
}

func (e *RemoteError) Unwrap() error { return e.Cause }

// IsNetError reports whether the failure was a transport-level problem. Such
// failures must never be read as "the file is not there".
func (e *RemoteError) IsNetError() bool {
	return e.ErrType == pcserror.ErrTypeNetError
}

// wrapPCSError converts an upstream error into this package's error type,
// mapping the "path does not exist" codes onto NotFoundError.
func wrapPCSError(op, path string, err pcserror.Error) error {
	if err == nil {
		return nil
	}
	code := err.GetRemoteErrCode()
	if isNotExistCode(code) || isNotExistMessage(err.GetRemoteErrMsg()) {
		return &NotFoundError{Path: path, Cause: err}
	}
	return &RemoteError{
		Op:      op,
		Path:    path,
		ErrType: err.GetErrType(),
		Code:    code,
		Msg:     err.GetRemoteErrMsg(),
		Cause:   err,
	}
}

// isNotExistCode lists the "no such file" codes seen across the PCS and Pan
// APIs used by this project (see baidupcs/pcserror translations).
func isNotExistCode(code int) bool {
	switch code {
	case -9, -3, 3, 31066:
		return true
	}
	return false
}

func isNotExistMessage(msg string) bool {
	switch msg {
	case "文件不存在", "目录不存在", "文件或目录不存在", "not exist", "file not found":
		return true
	}
	return false
}

// isAuthErrorCode lists the codes Baidu returns when the session is no longer
// valid. A scan that hits one of these MUST stop immediately: continuing would
// record every directory it could not read as effectively empty, and the
// planner would then treat their entire contents as locally-deleted and
// schedule them for removal.
func isAuthErrorCode(code int) bool {
	switch code {
	case -6, // 请重新登录
		-7,    // 未登录 / 登录失效
		31045, // user not exists / 登录过期
		31064, // 未登录
		31066: // 文件不存在 (仅当与登录消息同时出现时才算认证问题)
		return code != 31066
	}
	return false
}

// isAuthErrorMessage catches the same condition when only a message is available.
func isAuthErrorMessage(msg string) bool {
	switch {
	case strings.Contains(msg, "请重新登录"),
		strings.Contains(msg, "登录过期"),
		strings.Contains(msg, "重新登陆"),
		strings.Contains(msg, "user not exists"),
		strings.Contains(msg, "not authorized"):
		return true
	}
	return false
}

// AuthExpiredError means the Baidu session stopped being valid part-way through
// a run. It is returned instead of a partial result on purpose: a partial tree
// from an expired session looks exactly like "the user deleted half their files",
// which is the most dangerous possible input to a mirror.
type AuthExpiredError struct {
	// At is the cloud path whose listing first reported the failure.
	At string
	// Cause is the underlying authentication error.
	Cause error
}

func (e *AuthExpiredError) Error() string {
	return fmt.Sprintf("登录态在枚举过程中失效 (首次出现于 %s): %v; 请重新登录后重试, 本次结果不可用于对齐",
		e.At, e.Cause)
}

func (e *AuthExpiredError) Unwrap() error { return e.Cause }

// IsAuthExpired reports whether err is an AuthExpiredError.
func IsAuthExpired(err error) bool {
	var a *AuthExpiredError
	return errors.As(err, &a)
}

// IsAuthError reports whether err means the session became invalid. Callers must
// treat this as fatal for the whole run rather than as one unreadable subtree.
func IsAuthError(err error) bool {
	if err == nil {
		return false
	}
	var re *RemoteError
	if errors.As(err, &re) {
		return isAuthErrorCode(re.Code) || isAuthErrorMessage(re.Msg)
	}
	return isAuthErrorMessage(err.Error())
}

// IsNotFound reports whether err means "the cloud path is absent".
func IsNotFound(err error) bool {
	var nf *NotFoundError
	return errors.As(err, &nf)
}

// IsUnreachable reports whether err means "we could not find out" — a network
// failure, an auth failure, or an unexpected remote error. Reconcile treats this
// as fatal for the whole run: an unreachable subtree must never be planned as
// "everything below it is redundant".
func IsUnreachable(err error) bool {
	if err == nil {
		return false
	}
	if IsNotFound(err) {
		return false
	}
	var re *RemoteError
	if errors.As(err, &re) {
		return true
	}
	return true
}
