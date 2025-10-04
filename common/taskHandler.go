package common

import (
	"io"
	"log"
	"net"
	"strings"
	"sync"
)

type bufferedConn struct {
	net.Conn
	rbuf []byte
	wbuf []byte
}

func newBufferedConn(conn net.Conn, bufferSize int) *bufferedConn {
	return &bufferedConn{
		Conn: conn,
		rbuf: make([]byte, 0, bufferSize),
		wbuf: make([]byte, 0, bufferSize),
	}
}

func (bc *bufferedConn) Write(b []byte) (int, error) {
	bc.wbuf = append(bc.wbuf, b...)
	return len(b), nil
}

func (bc *bufferedConn) Flush() error {
	if len(bc.wbuf) == 0 {
		return nil
	}

	n, err := bc.Conn.Write(bc.wbuf)
	if err != nil {
		return err
	}

	if n < len(bc.wbuf) {
		bc.wbuf = bc.wbuf[n:]
		return io.ErrShortWrite
	}

	bc.wbuf = bc.wbuf[:0]
	return nil
}

func TaskHandler(a, b net.Conn, dstName, srcName, taskID string, bufferSize int) {
	var closeOnce sync.Once
	var wg sync.WaitGroup
	wg.Add(2)

	bufA := newBufferedConn(a, bufferSize)
	bufB := newBufferedConn(b, bufferSize)

	closeConn := func() {
		_ = bufA.Flush()
		_ = bufB.Flush()

		_ = a.Close()
		_ = b.Close()
		log.Printf("[%s] %s<->%s 连接已关闭", taskID, dstName, srcName)
	}

	go func() {
		defer func() {
			wg.Done()
			closeOnce.Do(closeConn)
		}()

		buf := make([]byte, bufferSize)
		total := 0

		for {
			n, err := b.Read(buf)
			if n > 0 {
				_, writeErr := bufA.Write(buf[:n])
				if writeErr != nil {
					log.Printf("[%s] %s->%s 写入缓冲区失败: %v", taskID, srcName, dstName, writeErr)
					return
				}
				if err := bufA.Flush(); err != nil {
					log.Printf("[%s] %s->%s 刷新缓冲区失败: %v", taskID, srcName, dstName, err)
					return
				}
				total += n
				log.Printf("[%s] %s->%s 转发数据: %d 字节 (累计: %d)", taskID, srcName, dstName, n, total)
			}
			if err != nil {
				if IsClosedError(err) {
					log.Printf("[%s] %s->%s 连接已关闭，转发完成，共 %d 字节", taskID, srcName, dstName, total)
				} else {
					log.Printf("[%s] %s->%s 读取异常: %v，已转发 %d 字节", taskID, srcName, dstName, err, total)
				}
				return
			}
		}
	}()

	go func() {
		defer func() {
			wg.Done()
			closeOnce.Do(closeConn)
		}()

		buf := make([]byte, bufferSize)
		total := 0

		for {
			n, err := a.Read(buf)
			if n > 0 {
				_, writeErr := bufB.Write(buf[:n])
				if writeErr != nil && !IsClosedError(writeErr) {
					log.Printf("[%s] %s->%s 写入缓冲区失败: %v", taskID, dstName, srcName, writeErr)
					return
				}
				if err := bufB.Flush(); err != nil && !IsClosedError(writeErr) {
					log.Printf("[%s] %s->%s 刷新缓冲区失败: %v", taskID, dstName, srcName, err)
					return
				}
				total += n
				log.Printf("[%s] %s->%s 转发数据: %d 字节 (累计: %d)", taskID, dstName, srcName, n, total)
			}
			if err != nil {
				if IsClosedError(err) {
					log.Printf("[%s] %s->%s 连接已关闭，转发完成，共 %d 字节", taskID, dstName, srcName, total)
				} else {
					log.Printf("[%s] %s->%s 读取异常: %v，已转发 %d 字节", taskID, dstName, srcName, err, total)
				}
				return
			}
		}
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
