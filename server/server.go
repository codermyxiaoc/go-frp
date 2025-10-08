package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"frp-project/common"
	"github.com/spf13/viper"
	"log"
	"net"
	"strconv"
	"sync"
	"time"
)

type Config struct {
	ConnChanCount int    `mapstructure:"conn-chan-count"`
	IdleTimeout   int64  `mapstructure:"idle-timeout"`
	BufferSize    int    `mapstructure:"buffer-size"`
	KeepAliveTime int    `mapstructure:"keep-alive-time"`
	Secret        string `mapstructure:"secret"`
	MainPort      string `mapstructure:"main-port"`
}

var config Config

func init() {
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)
	v := viper.New()
	v.SetConfigName("config")
	v.AddConfigPath(".")
	v.SetDefault("buffer-size", 5)
	v.SetDefault("idle-timeout", 30)
	v.SetDefault("conn-chan-count", 100)
	v.SetDefault("keep-alive-time", 10)
	v.SetDefault("secret", "secret")
	v.SetDefault("main-port", "11234")
	if err := v.ReadInConfig(); err != nil {
		log.Printf("读取配置文件失败: %v", err)
	}
	if err := v.Unmarshal(&config); err != nil {
		log.Printf("解析配置文件失败: %v", err)
	}
}

func main() {
	log.Println("=== 启动服务端 ===")
	mainListen, err := net.Listen("tcp", fmt.Sprintf(":%s", config.MainPort))
	defer func() { _ = mainListen.Close() }()
	if err != nil {
		log.Printf("主服务器监听失败: %v", err)
		return
	}
	for {
		log.Println("等待客户端连接主服务...")
		mainConn, err := mainListen.Accept()
		if err != nil {
			log.Printf("客户端连接主服务失败: %v", err)
			continue
		}

		// 密钥验证
		go func() {
			defer func() {
				err := recover()
				if err != nil {
					log.Println(err)
					_ = mainConn.Close()
				}
			}()
			err = mainConn.SetReadDeadline(time.Now().Add(10 * time.Second))
			if err != nil {
				panic(errors.New(fmt.Sprintf("设置密钥读取超时失败: %v", err)))
			}
			reader := bufio.NewReader(mainConn)
			clientSecret, err := reader.ReadString(common.DELIM)
			if err != nil {
				panic(errors.New(fmt.Sprintf("读取客户端密钥失败: %v", err)))
			}
			if clientSecret[0:len(config.Secret)] != config.Secret {
				_, _ = mainConn.Write([]byte("00000"))
				panic(fmt.Sprintf("密钥错误[%s]（%s）", clientSecret, mainConn.RemoteAddr().String()))
			}
			err = mainConn.SetDeadline(time.Time{})
			if err != nil {
				panic(errors.New(fmt.Sprintf("重置main连接超时失败: %v", err)))
			}
			go initService(mainConn)
		}()

	}
}

func initService(mainConn net.Conn) {
	exitChan := make(chan struct{})

	log.Println("=== 开始初始化客户端连接服务端 ===")
	masterListen, err := net.Listen("tcp", ":0")
	port := masterListen.Addr().(*net.TCPAddr).Port
	log.Printf("服务端master连接监听启动成功: %d", port)

	_, err = mainConn.Write([]byte(strconv.Itoa(port)))
	if err != nil {
		log.Printf("发送master连接端口失败指令失败（%s）: %v", mainConn.RemoteAddr(), err)
		_ = mainConn.Close()
		_ = masterListen.Close()
		return
	}
	_ = mainConn.Close()
	log.Printf("成发送master连接端口[%d]指令成功（%s）", port, mainConn.RemoteAddr())

	masterConn, err := masterListen.Accept()
	if err != nil {
		log.Printf("主连接失败: %v", err)
		_ = masterListen.Close()
		return
	}
	log.Printf("客户端主连接建立成功: %s", masterConn.RemoteAddr())

	ctx, cancel := context.WithCancel(context.Background())
	informChan := make(chan struct{}, config.ConnChanCount)
	connChan := make(chan net.Conn, config.ConnChanCount)
	var wg sync.WaitGroup

	wg.Add(3)
	go inform(masterConn, informChan, exitChan, &wg)
	go acceptWeb(connChan, informChan, ctx, masterConn, &wg)
	go acceptTask(masterListen, connChan, &wg)

	select {
	case <-exitChan:
		cancel()
		_ = masterListen.Close()
		_ = masterConn.Close()
		wg.Wait()
		close(informChan)
		close(connChan)
		log.Println("当前连接资源已清理，连接结束")
		return
	}
}

func inform(masterConn net.Conn, informChan <-chan struct{}, exitChan chan<- struct{}, wg *sync.WaitGroup) {
	defer func() {
		log.Println("=== 收到退出信号，开始清除资源 ===")
		log.Println("inform 协程已退出")
		wg.Done()
		exitChan <- struct{}{}
	}()

	ticker := time.NewTicker(time.Duration(config.KeepAliveTime) * time.Second)
	defer ticker.Stop()

	remoteAddr := masterConn.RemoteAddr().String()
	for {
		select {
		case _, ok := <-informChan:
			if !ok {
				log.Printf("informChan 已关闭，inform 协程退出")
				return
			}
			n, err := masterConn.Write([]byte(common.NEW_TASK))
			if err != nil {
				log.Printf("发送new指令失败（%s）: %v", remoteAddr, err)
				return
			}
			log.Printf("发送new指令成功（%s）: 共 %d 字节", remoteAddr, n)

		case <-ticker.C:
			n, err := masterConn.Write([]byte(common.PI))
			if err != nil {
				log.Printf("发送心跳包失败（%s）: %v", remoteAddr, err)
				return
			}
			log.Printf("发送心跳包成功（%s）: 共 %d 字节", remoteAddr, n)
		}
	}
}

func acceptWeb(connChan chan<- net.Conn, informChan chan<- struct{}, ctx context.Context, masterConn net.Conn, wg *sync.WaitGroup) {
	defer func() {
		log.Println("acceptWeb 协程已退出")
		wg.Done()
	}()

	webListen, err := net.Listen("tcp", fmt.Sprintf(":0"))
	if err != nil {
		log.Printf("web监听启动失败: %v", err)
		return
	}
	go func() {
		<-ctx.Done()
		_ = webListen.Close()
	}()

	webPort := webListen.Addr().(*net.TCPAddr).Port
	log.Printf("web端监听启动成功: :%d", webPort)
	_, err = masterConn.Write([]byte(fmt.Sprintf(":%d%c", webPort, common.DELIM)))
	if err != nil {
		log.Printf("发送web端口失败指令失败（%s）: %v", masterConn.RemoteAddr(), err)
		_ = masterConn.Close()
		return
	}

	for {
		webConn, err := webListen.Accept()
		if err != nil {
			var opErr *net.OpError
			if errors.As(err, &opErr) && opErr.Op == "accept" && opErr.Err.Error() == "use of closed network connection" {
				log.Println("web监听已关闭，acceptWeb 正常退出")
				return
			}
			log.Printf("web端接收连接失败: %v", err)
			continue
		}

		webAddr := webConn.RemoteAddr().String()
		log.Printf("web端连接建立成功: %s", webAddr)

		go func() {
			select {
			case connChan <- webConn:
				select {
				case informChan <- struct{}{}:
					log.Printf("已通知主客户端：新web连接（%s）", webAddr)
				default:
					log.Printf("informChan 已满，无法通知新web连接（%s）", webAddr)
					_ = webConn.Close()
				}
			default:
				log.Printf("connChan 已满，关闭新web连接（%s）", webAddr)
				_ = webConn.Close()
			}
		}()
	}
}

func acceptTask(masterListen net.Listener, connChan <-chan net.Conn, wg *sync.WaitGroup) {
	defer func() {
		log.Println("acceptTask 协程已退出")
		wg.Done()
	}()

	for {
		taskConn, err := masterListen.Accept()
		if err != nil {
			var opErr *net.OpError
			if errors.As(err, &opErr) && opErr.Op == "accept" && opErr.Err.Error() == "use of closed network connection" {
				log.Println("主监听已关闭，acceptTask 正常退出")
				return
			}
			log.Printf("接收任务连接失败: %v", err)
			continue
		}
		taskAddr := taskConn.RemoteAddr().String()
		log.Printf("任务连接建立成功: %s", taskAddr)

		go func(taskConn net.Conn) {
			select {
			case webConn, ok := <-connChan:
				if !ok {
					log.Printf("connChan 已关闭，关闭任务连接（%s）", taskAddr)
					_ = taskConn.Close()
					return
				}
				taskID := fmt.Sprintf("task-%d", time.Now().UnixNano())
				log.Printf("任务配对成功: %s（web: %s, task: %s）", taskID, webConn.RemoteAddr(), taskAddr)
				go common.Transform(taskConn, webConn, "task", "web", taskID, config.BufferSize*1024, config.IdleTimeout)
			case <-time.After(30 * time.Second):
				log.Printf("任务连接（%s）30秒内无web连接配对，已关闭", taskAddr)
				_ = taskConn.Close()
			}
		}(taskConn)
	}
}
