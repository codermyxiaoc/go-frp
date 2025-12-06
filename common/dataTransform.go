package common

import (
	"github.com/sirupsen/logrus"
	"golang.org/x/time/rate"
	"io"
	"net"
	"strings"
	"sync"
)

type TransformConfig struct {
	DstName              string
	SrcName              string
	EnableLongConnection bool
	ConnectionTimeout    int64
	EnableLimit          bool
	LimitBufferSize      int
}

var BufferPool = sync.Pool{
	New: func() interface{} {
		return make([]byte, 32*1024)
	},
}

func Transform(dstConn, srcConn net.Conn, config TransformConfig) {
	if tcpConn, ok := dstConn.(*net.TCPConn); ok {
		_ = tcpConn.SetNoDelay(true)
	}
	if tcpConn, ok := srcConn.(*net.TCPConn); ok {
		_ = tcpConn.SetNoDelay(true)
	}

	var closeOnce sync.Once
	var wg sync.WaitGroup
	wg.Add(2)

	closeConn := func() {
		_ = dstConn.Close()
		_ = srcConn.Close()
	}

	var monitoredDst io.ReadWriter = dstConn
	var monitoredSrc io.ReadWriter = srcConn

	if !config.EnableLongConnection {
		session := NewSession(config.ConnectionTimeout, &closeConn)
		defer session.Close()
		monitoredDst = NewMonitored(dstConn, session)
		monitoredSrc = NewMonitored(srcConn, session)
		go session.monitorIdle()
	}

	if config.EnableLimit {
		monitoredDst = NewRateLimited(monitoredDst, rate.NewLimiter(rate.Limit(config.LimitBufferSize), config.LimitBufferSize))
		monitoredSrc = NewRateLimited(monitoredSrc, rate.NewLimiter(rate.Limit(config.LimitBufferSize), config.LimitBufferSize))
	}

	bufA := BufferPool.Get().([]byte)
	defer BufferPool.Put(bufA)
	bufB := BufferPool.Get().([]byte)
	defer BufferPool.Put(bufB)

	go func() {
		defer func() {
			wg.Done()
			closeOnce.Do(closeConn)
		}()

		_, err := io.CopyBuffer(monitoredDst, monitoredSrc, bufA)
		if err != nil {
			if !IsClosedError(err) {
				logrus.Errorf("%s->%s 转发异常: %v", config.DstName, config.SrcName, err)
			}
		}
	}()

	go func() {
		defer func() {
			wg.Done()
			closeOnce.Do(closeConn)
		}()

		_, err := io.CopyBuffer(monitoredSrc, monitoredDst, bufB)
		if err != nil {
			if !IsClosedError(err) {
				logrus.Errorf("%s->%s 转发异常: %v", config.DstName, config.SrcName, err)
			}
		}
	}()
	wg.Wait()
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
		strings.Contains(errMsg, "read: connection reset by peer") ||
		strings.Contains(errMsg, "write: broken pipe") ||
		strings.Contains(errMsg, "wsasend: An established connection was aborted by the software in your host machine") ||
		strings.Contains(errMsg, "wsarecv: An existing connection was forcibly closed by the remote host")
}
