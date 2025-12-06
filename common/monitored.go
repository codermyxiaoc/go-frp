package common

import (
	"net"
	"sync"
	"sync/atomic"
	"time"
)

type Session struct {
	lastActivity      int64
	connectionTimeout int64
	closeChan         chan struct{}
	rollback          *func()
	rollbackOnce      sync.Once
	taskId            string
}

func NewSession(connectionTimeout int64, rollback *func()) *Session {
	return &Session{
		connectionTimeout: connectionTimeout,
		closeChan:         make(chan struct{}),
		rollbackOnce:      sync.Once{},
		lastActivity:      time.Now().Unix(),
		rollback:          rollback,
	}
}

func (session *Session) monitorIdle() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			lastActivity := atomic.LoadInt64(&session.lastActivity)
			idleTime := time.Now().Unix() - lastActivity

			if idleTime > session.connectionTimeout {
				session.rollbackOnce.Do(*session.rollback)
				return
			}
		case <-session.closeChan:
			return
		}
	}
}

func (session *Session) updateActivity() {
	secCurrent := time.Now().Unix()
	lastActivity := atomic.LoadInt64(&session.lastActivity)
	if secCurrent > lastActivity {
		atomic.StoreInt64(&session.lastActivity, secCurrent)
	}
}

func (session *Session) Close() {
	select {
	case <-session.closeChan:
	default:
		close(session.closeChan)
	}
}

type Monitored struct {
	Conn    net.Conn
	session *Session
}

func NewMonitored(conn net.Conn, session *Session) *Monitored {
	monitored := Monitored{
		Conn:    conn,
		session: session,
	}
	return &monitored
}

func (m *Monitored) Read(p []byte) (n int, err error) {
	n, err = m.Conn.Read(p)
	if n > 0 {
		m.session.updateActivity()
	}
	return
}

func (m *Monitored) Write(p []byte) (n int, err error) {
	n, err = m.Conn.Write(p)
	if n > 0 {
		m.session.updateActivity()
	}
	return
}
