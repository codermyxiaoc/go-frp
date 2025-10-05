package common

import (
	"io"
	"log"
	"net"
	"strings"
	"sync"
)

func TaskHandler(a, b net.Conn, dstName, srcName, taskID string, bufferSize int) {
	var closeOnce sync.Once
	var wg sync.WaitGroup
	wg.Add(2)

	closeConn := func() {
		_ = a.Close()
		_ = b.Close()
		log.Printf("[%s] %s<->%s 连接已关闭", taskID, dstName, srcName)
	}

	go func() {
		defer func() {
			wg.Done()
			closeOnce.Do(closeConn)
		}()

		written, err := io.CopyBuffer(a, b, make([]byte, bufferSize))
		if err != nil {
			if !IsClosedError(err) {
				log.Printf("[%s] %s->%s 转发异常: %v", taskID, srcName, dstName, err)
			}
		}
		log.Printf("[%s] %s->%s 转发完成，共 %d 字节", taskID, srcName, dstName, written)
	}()

	go func() {
		defer func() {
			wg.Done()
			closeOnce.Do(closeConn)
		}()

		written, err := io.CopyBuffer(b, a, make([]byte, bufferSize))
		if err != nil {
			if !IsClosedError(err) {
				log.Printf("[%s] %s->%s 转发异常: %v", taskID, dstName, srcName, err)
			}
		}
		log.Printf("[%s] %s->%s 转发完成，共 %d 字节", taskID, dstName, srcName, written)
	}()

	wg.Wait()
	log.Printf("[%s] %s<->%s 任务处理完成", taskID, dstName, srcName)
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
