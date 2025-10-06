package main

import (
	"errors"
	"fmt"
	"frp-project/common"
	"github.com/spf13/viper"
	"log"
	"net"
	"os"
	"os/signal"
	"time"
)

type Config struct {
	ServerPort    string `mapstructure:"server-port"`
	ServerIp      string `mapstructure:"server-ip"`
	LocalPort     string `mapstructure:"local-port"`
	BufferSize    int    `mapstructure:"buffer-size"`
	KeepAliveTime int    `mapstructure:"keep-alive-time"`
	IdleTimeout   int64  `mapstructure:"idle-timeout"`
}

var config Config

func init() {
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)
	v := viper.New()
	v.SetConfigName("config")
	v.AddConfigPath(".")
	v.SetDefault("port", "8090")
	v.SetDefault("server-port", "8080")
	v.SetDefault("keep-alive-time", 10)
	v.SetDefault("buffer-size", 5)
	v.SetDefault("idle-timeout", 30)
	v.SetDefault("server-ip", "127.0.0.1")
	if err := v.ReadInConfig(); err != nil {
		log.Printf("读取配置文件失败: %v", err)
	}
	if err := v.Unmarshal(&config); err != nil {
		log.Printf("解析配置文件失败: %v", err)

	}
}

func main() {
	log.Println("客户端启动，尝试连接服务器...")
	dialer := net.Dialer{Timeout: 10 * time.Second}
	masterConn, err := dialer.Dial("tcp", fmt.Sprintf("%s:%s", config.ServerIp, config.ServerPort))
	if err != nil {
		log.Printf("连接服务器失败: %v", err)
		return
	}
	log.Printf("成功连接到服务器: %s", masterConn.RemoteAddr())

	go keepAlive(masterConn)
	go inform(masterConn)

	defer func() {
		if err := masterConn.Close(); err != nil {
			log.Printf("关闭主连接失败: %v", err)
		} else {
			log.Println("主连接已关闭")
		}
	}()

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt)
	<-signals
	log.Println("程序退出")
}

func keepAlive(masterConn net.Conn) {
	ticker := time.NewTicker(time.Duration(config.KeepAliveTime) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			_, err := masterConn.Write([]byte("pi"))
			if err != nil {
				log.Printf("发送心跳包失败: %v，尝试重连...", err)
				return
			}
		}
	}
}

func inform(masterConn net.Conn) {
	buffer := make([]byte, 3)
	for {
		if err := masterConn.SetReadDeadline(time.Now().Add(60 * time.Second)); err != nil {
			log.Printf("设置读取超时失败: %v", err)
			return
		}
		read, err := masterConn.Read(buffer)
		if err != nil {
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				log.Println("读取超时，继续等待数据")
				continue
			}
			log.Printf("读取数据失败: %v", err)
			return
		}
		log.Printf("接收到服务器数据: %s, 长度: %d", string(buffer[:read]), read)
		if read == 3 && string(buffer[:3]) == "new" {
			log.Println("接收到新任务指令，启动任务处理器")
			go taskHandler()
		}
	}
}

func taskHandler() {
	dialer := net.Dialer{Timeout: 10 * time.Second}
	serverConn, err := dialer.Dial("tcp", fmt.Sprintf("%s:%s", config.ServerIp, config.ServerPort))

	if err != nil {
		log.Printf("任务连接服务器失败: %v", err)
		return
	}
	log.Printf("任务成功连接到服务器: %s", serverConn.RemoteAddr())

	localConn, err := dialer.Dial("tcp", fmt.Sprintf(":%s", config.LocalPort))
	if err != nil {
		log.Printf("任务连接本地服务失败: %v", serverConn.Close())
		return
	}
	log.Printf("任务成功连接到本地服务: %s", localConn.RemoteAddr())

	taskID := fmt.Sprintf("task-%d", time.Now().UnixNano())
	log.Printf("启动任务处理: %s", taskID)

	common.Transform(localConn, serverConn, "local", "server", taskID, config.BufferSize*1024*1024, config.IdleTimeout)

	log.Printf("任务处理完成: %s", taskID)
}
