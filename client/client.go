package main

import (
	"bufio"
	"errors"
	"fmt"
	"github.com/spf13/viper"
	"go-frp/common"
	"log"
	"net"
	"sync"
	"time"
)

type Config struct {
	ServerIp      string `mapstructure:"server-ip"`
	LocalPort     string `mapstructure:"local-port"`
	BufferSize    int    `mapstructure:"buffer-size"`
	KeepAliveTime int    `mapstructure:"keep-alive-time"`
	IdleTimeout   int64  `mapstructure:"idle-timeout"`
	Secret        string `mapstructure:"secret"`
	MainPort      string `mapstructure:"main-port"`
}

var config Config

func init() {
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)
	v := viper.New()
	v.SetConfigName("config")
	v.AddConfigPath(".")
	v.SetDefault("port", "8090")
	v.SetDefault("keep-alive-time", 10)
	v.SetDefault("buffer-size", 5)
	v.SetDefault("idle-timeout", 30)
	v.SetDefault("server-ip", "127.0.0.1")
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
	defer log.Println("客户端程序退出")
	log.Println("客户端启动，尝试连接服务器...")

	mainDialer := net.Dialer{Timeout: 10 * time.Second}
	mainConn, err := mainDialer.Dial("tcp", fmt.Sprintf("%s:%s", config.ServerIp, config.MainPort))
	defer func() { _ = mainConn.Close() }()
	if err != nil {
		log.Printf("连接服务器主连接失败: %v", err)
		return
	}
	log.Println("成功连接到服务器主链接")

	_, err = mainConn.Write(append([]byte(config.Secret), common.DELIM))
	if err != nil {
		log.Printf("发送密钥失败: %v", err)
		return
	}
	masterPort := make([]byte, 5)
	_, err = mainConn.Read(masterPort)
	if err != nil {
		log.Printf("读取服务器端口失败: %v", err)
		return
	}
	if string(masterPort) == "00000" {
		log.Printf("密钥错误[%s]", config.Secret)
		return
	}
	initClient(string(masterPort))
}

func initClient(masterAndTaskPort string) {
	dialer := net.Dialer{Timeout: 10 * time.Second}
	masterConn, err := dialer.Dial("tcp", fmt.Sprintf("%s:%s", config.ServerIp, masterAndTaskPort))
	if err != nil {
		log.Printf("连接服务器失败: %v", err)
		return
	}
	log.Printf("成功连接到服务器: %s", masterConn.RemoteAddr())

	var wg sync.WaitGroup
	wg.Add(2)

	go keepAlive(masterConn, &wg)
	go inform(masterConn, masterAndTaskPort, &wg)

	wg.Wait()
	defer func() {
		if err := masterConn.Close(); err != nil {
			log.Printf("关闭主连接失败: %v", err)
		} else {
			log.Println("主连接已关闭")
		}
	}()
}

func keepAlive(masterConn net.Conn, wg *sync.WaitGroup) {
	ticker := time.NewTicker(time.Duration(config.KeepAliveTime) * time.Second)
	defer func() {
		ticker.Stop()
		wg.Done()
	}()

	for {
		select {
		case <-ticker.C:
			_, err := masterConn.Write([]byte(common.PI))
			if err != nil {
				log.Printf("发送心跳包失败: %v", err)
				return
			}
		}
	}
}

func inform(masterConn net.Conn, taskPort string, wg *sync.WaitGroup) {
	defer wg.Done()
	reader := bufio.NewReader(masterConn)
	for {
		if err := masterConn.SetReadDeadline(time.Now().Add(60 * time.Second)); err != nil {
			log.Printf("设置读取超时失败: %v", err)
			return
		}
		readString, err := reader.ReadString(common.DELIM)
		if err != nil {
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				log.Println("读取超时，继续等待数据")
				continue
			}
			log.Printf("读取数据失败: %v", err)
			return
		}
		n := len(readString)
		if n == 0 {
			continue
		}
		log.Printf("接收到服务器数据: %s, 长度: %d", readString[:n-1], n)
		switch {
		case n == common.PI_LEN && readString == common.PI:
			continue
		case n == common.NEW_TASK_LEN && readString[:common.NEW_TASK_LEN] == common.NEW_TASK:
			log.Println("接收到新任务指令，启动任务处理器")
			go taskHandler(taskPort)
			continue
		case readString[:1] == ":":
			log.Printf("web访问地址: http://%s%s", config.ServerIp, readString[:n-1])
		}

	}
}

func taskHandler(taskPort string) {
	dialer := net.Dialer{Timeout: 10 * time.Second}
	serverConn, err := dialer.Dial("tcp", fmt.Sprintf("%s:%s", config.ServerIp, taskPort))

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

	go common.Transform(localConn, serverConn, "local", "server", taskID, config.BufferSize*1024, config.IdleTimeout)
}
