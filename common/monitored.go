package common

import (
	"net"
	"sync"
	"time"
)

type Session struct {
	mu           sync.Mutex
	lastActivity int64
	idleTimeout  int64
	closeChan    chan struct{}
	rollback     *func()
	rollbackOnce sync.Once
	taskId       string
}

func NewSession(idleTimeout int64, rollback *func()) *Session {
	return &Session{
		idleTimeout:  idleTimeout,
		closeChan:    make(chan struct{}),
		rollbackOnce: sync.Once{},
		lastActivity: time.Now().Unix(),
		rollback:     rollback,
	}
}

func (session *Session) monitorIdle() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			lockBeforeTime := time.Now().Unix()
			session.mu.Lock()
			idleTime := lockBeforeTime - session.lastActivity
			session.mu.Unlock()
			if idleTime > session.idleTimeout {
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
	if secCurrent == session.lastActivity {
		return
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.lastActivity >= secCurrent {
		return
	}
	session.lastActivity = secCurrent
}
func (session *Session) Close() {
	select {
	case <-session.closeChan:
	default:
		close(session.closeChan)
	}
}

type Monitored struct {
	Conn    *net.Conn
	session *Session
}

func NewMonitored(conn *net.Conn, session *Session) *Monitored {
	monitored := Monitored{
		Conn:    conn,
		session: session,
	}
	return &monitored
}

func (m *Monitored) Read(p []byte) (n int, err error) {
	n, err = (*m.Conn).Read(p)
	if n > 0 {
		m.session.updateActivity()
	}
	return
}

func (m *Monitored) Write(p []byte) (n int, err error) {
	n, err = (*m.Conn).Write(p)
	if n > 0 {
		m.session.updateActivity()
	}
	return
}
