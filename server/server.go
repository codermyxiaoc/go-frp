package main

import (
	"context"
	"errors"
	"fmt"
	"frp-project/common"
	"github.com/spf13/viper"
	"log"
	"net"
	"sync"
	"time"
)

type Config struct {
	ServerPort    string `mapstructure:"server-port"`
	WebPort       string `mapstructure:"web-port"`
	ConnChanCount int    `mapstructure:"conn-chan-count"`
	IdleTimeout   int64  `mapstructure:"idle-timeout"`
	BufferSize    int    `mapstructure:"buffer-size"`
	KeepAliveTime int    `mapstructure:"keep-alive-time"`
}

var config Config

func init() {
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)
	v := viper.New()
	v.SetConfigName("config")
	v.AddConfigPath(".")
	v.SetDefault("server-port", "8080")
	v.SetDefault("web-port", "8088")
	v.SetDefault("buffer-size", 5)
	v.SetDefault("idle-timeout", 30)
	v.SetDefault("conn-chan-count", 100)
	v.SetDefault("keep-alive-time", 10)
	if err := v.ReadInConfig(); err != nil {
		log.Printf("读取配置文件失败: %v", err)
	}
	if err := v.Unmarshal(&config); err != nil {
		log.Printf("解析配置文件失败: %v", err)
	}
}

func main() {
	reconnectChan := make(chan struct{})

	for {
		log.Println("=== 开始初始化服务端 ===")
		listen, err := net.Listen("tcp", ":8080")
		log.Printf("服务端监听启动成功: :%s", config.ServerPort)

		masterConn, err := listen.Accept()
		if err != nil {
			log.Printf("主连接失败: %v", err)
			_ = listen.Close()
			continue
		}
		log.Printf("客户端主连接建立成功: %s", masterConn.RemoteAddr())

		ctx, cancel := context.WithCancel(context.Background())
		informChan := make(chan struct{}, config.ConnChanCount)
		connChan := make(chan net.Conn, config.ConnChanCount)
		var wg sync.WaitGroup

		wg.Add(3)
		go inform(masterConn, informChan, reconnectChan, &wg)
		go acceptWeb(connChan, informChan, ctx, &wg)
		go acceptTask(listen, connChan, &wg)

		select {
		case <-reconnectChan:
			cancel()
			_ = listen.Close()
			_ = masterConn.Close()
			wg.Wait()
			close(informChan)
			close(connChan)
			log.Println("当前连接资源已清理，准备重连...")
			continue
		}
	}
}

func inform(masterConn net.Conn, informChan <-chan struct{}, reconnectChan chan<- struct{}, wg *sync.WaitGroup) {
	defer func() {
		log.Println("=== 收到重连信号，开始重启服务 ===")
		log.Println("inform 协程已退出")
		wg.Done()
		reconnectChan <- struct{}{}
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
			n, err := masterConn.Write([]byte("new"))
			if err != nil {
				log.Printf("发送new指令失败（%s）: %v", remoteAddr, err)
				return
			}
			log.Printf("发送new指令成功（%s）: 共 %d 字节", remoteAddr, n)

		case <-ticker.C:
			n, err := masterConn.Write([]byte("pi"))
			if err != nil {
				log.Printf("发送心跳包失败（%s）: %v", remoteAddr, err)
				return
			}
			log.Printf("发送心跳包成功（%s）: 共 %d 字节", remoteAddr, n)
		}
	}
}

func acceptWeb(connChan chan<- net.Conn, informChan chan<- struct{}, ctx context.Context, wg *sync.WaitGroup) {
	defer func() {
		log.Println("acceptWeb 协程已退出")
		wg.Done()
	}()

	webListen, err := net.Listen("tcp", fmt.Sprintf(":%s", config.WebPort))
	if err != nil {
		log.Printf("web监听启动失败: %v", err)
		return
	}
	log.Printf("web端监听启动成功: :%s", config.WebPort)

	go func() {
		<-ctx.Done()
		_ = webListen.Close()
	}()

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

func acceptTask(mainListen net.Listener, connChan <-chan net.Conn, wg *sync.WaitGroup) {
	defer func() {
		log.Println("acceptTask 协程已退出")
		wg.Done()
	}()

	for {
		taskConn, err := mainListen.Accept()
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
				go common.Transform(taskConn, webConn, "task", "web", taskID, config.BufferSize*1024*1024, config.IdleTimeout)
			case <-time.After(30 * time.Second):
				log.Printf("任务连接（%s）30秒内无web连接配对，已关闭", taskAddr)
				_ = taskConn.Close()
			}
		}(taskConn)
	}
}
