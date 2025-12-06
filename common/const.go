package common

var (
	NEW_TASK            = "new\n"
	PI                  = "pi\n"
	NEW_TASK_LEN        = len(NEW_TASK)
	PI_LEN              = len(PI)
	DELIM               = byte('\n')
	SYS_LOG_PATH        = "./logs/frp.log"
	LOCAL_SERVICE_ERROR = []byte("HTTP/1.1 502 Bad Gateway\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"Content-Length: 36\r\n" +
		"Connection: close\r\n" +
		"\r\n" +
		"连接本地服务超时")
	WEB_CHAN_ERROR = []byte("HTTP/1.1 503 Service Unavailable\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"Content-Length: 36\r\n" +
		"Connection: close\r\n" +
		"Retry-After: 5\r\n" +
		"\r\n" +
		"可用连接通道占满")
)
