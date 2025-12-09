package common

import (
	"errors"
	"io"
	"net"
	"strings"
)

func IsPortInUse(err error) bool {
	return strings.Contains(err.Error(), "Only one usage of each socket address") || strings.Contains(err.Error(), "address already in use")
}
func IsAcceptError(err error) bool {
	var opErr *net.OpError
	return errors.As(err, &opErr) && opErr.Op == "accept" && opErr.Err.Error() == "use of closed network connection"
}
func IsClosedError(err error) bool {
	if err == nil {
		return false
	}
	if err == io.EOF {
		return true
	}
	errMsg := err.Error()
	return strings.Contains(errMsg, "closed network connection") ||
		strings.Contains(errMsg, "use of closed network connection") ||
		strings.Contains(errMsg, "connection reset by peer") ||
		strings.Contains(errMsg, "broken pipe") ||
		strings.Contains(errMsg, "An established connection was aborted by the software in your host machine") ||
		strings.Contains(errMsg, "An existing connection was forcibly closed by the remote host")
}
