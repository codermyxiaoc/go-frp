package main

import (
	"bufio"
	"errors"
	"fmt"
	"github.com/sirupsen/logrus"
	"github.com/spf13/viper"
	"go-frp/common"
	"log"
	"net"
	"strings"
	"sync"
	"time"
)

type Config struct {
	ServerIp             string `mapstructure:"server-ip" json:"serverIp"`
	LocalPort            []int  `mapstructure:"local-port" json:"localPort"`
	BufferSize           int    `mapstructure:"buffer-size" json:"bufferSize"`
	KeepAliveTime        int    `mapstructure:"keep-alive-time" json:"keepAliveTime"`
	ConnectionTimeout    int64  `mapstructure:"connection-timeout" json:"connectionTimeout"`
	EnableLongConnection bool   `mapstructure:"enable-long-connection" json:"enableLongConnection"`
	Secret               string `mapstructure:"secret" json:"secret"`
	MainPort             string `mapstructure:"main-port" json:"mainPort"`
	EnableLimit          bool   `mapstructure:"enable-limit" json:"enableLimit"`
	LimitBufferSize      int    `mapstructure:"limit-buffer-size" json:"limitBufferSize"`
}

var config Config

func init() {
	common.InitLog()
	v := viper.New()
	v.SetConfigName("config")
	v.AddConfigPath(".")
	v.SetDefault("port", "8090")
	v.SetDefault("keep-alive-time", 10)
	v.SetDefault("buffer-size", 5)
	v.SetDefault("connection-timeout", 30)
	v.SetDefault("server-ip", "127.0.0.1")
	v.SetDefault("secret", "secret")
	v.SetDefault("main-port", "11234")
	v.SetDefault("enableLong-connection", true)
	v.SetDefault("enable-limit", true)
	v.SetDefault("limit-buffer-size", 1024)

	if err := v.ReadInConfig(); err != nil {
		log.Printf("读取配置文件失败: %v", err)
	}
	if err := v.Unmarshal(&config); err != nil {
		log.Printf("解析配置文件失败: %v", err)

	}
}

func main() {
	var wg sync.WaitGroup
	wg.Add(len(config.LocalPort))
	for _, port := range config.LocalPort {
		go initClient(port, &wg)
	}
	wg.Wait()
}

func initClient(port int, mainWg *sync.WaitGroup) {
	mainDialer := net.Dialer{Timeout: 10 * time.Second}
	mainConn, err := mainDialer.Dial("tcp", fmt.Sprintf("%s:%s", config.ServerIp, config.MainPort))
	defer func() { _ = mainConn.Close() }()
	if err != nil {
		logrus.Errorf("连接服务器主连接失败: %v", err)
		return
	}
	_, err = mainConn.Write(append([]byte(config.Secret), common.DELIM))
	if err != nil {
		logrus.Errorf("发送密钥失败: %v", err)
		return
	}
	masterPort := make([]byte, 5)
	_, err = mainConn.Read(masterPort)
	if err != nil {
		logrus.Errorf("读取服务器端口失败: %v", err)
		return
	}
	if string(masterPort) == "00000" {
		logrus.Errorf("密钥错误[%s]", config.Secret)
		return
	}
	go startServer(string(masterPort), port, mainWg)
}

func startServer(masterAndTaskPort string, localPort int, mainWg *sync.WaitGroup) {
	defer mainWg.Done()
	dialer := net.Dialer{Timeout: 10 * time.Second}
	masterConn, err := dialer.Dial("tcp", fmt.Sprintf("%s:%s", config.ServerIp, masterAndTaskPort))
	if err != nil {
		logrus.Errorf("连接服务器失败: %v", err)
		return
	}

	var wg sync.WaitGroup
	wg.Add(2)

	go keepAlive(masterConn, &wg)
	go inform(masterConn, masterAndTaskPort, localPort, &wg)

	wg.Wait()
	defer func() {
		if err := masterConn.Close(); err != nil {
			logrus.Errorf("关闭主连接失败: %v", err)
		} else {
			logrus.Errorf("主连接已关闭")
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
				logrus.Errorf("发送心跳包失败: %v", err)
				return
			}
		}
	}
}

func inform(masterConn net.Conn, taskPort string, localPort int, wg *sync.WaitGroup) {

	defer wg.Done()
	reader := bufio.NewReader(masterConn)
	for {
		if err := masterConn.SetReadDeadline(time.Now().Add(60 * time.Second)); err != nil {
			logrus.Errorf("设置读取超时失败: %v", err)
			return
		}
		readString, err := reader.ReadString(common.DELIM)
		if err != nil {
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				logrus.Errorf("读取超时，继续等待数据")
				continue
			}
			logrus.Errorf("读取数据失败: %v", err)
			return
		}
		n := len(readString)
		if n == 0 {
			continue
		}
		switch {
		case n == common.PI_LEN && readString == common.PI:
			continue
		case n == common.NEW_TASK_LEN && readString[:common.NEW_TASK_LEN] == common.NEW_TASK:
			go taskHandler(taskPort, localPort)
			continue
		case readString[:1] == ":":
			logrus.Printf("%s <-> %s 本地端口: %d web访问地址: http://%s%s", masterConn.RemoteAddr(), masterConn.LocalAddr(), localPort, config.ServerIp, readString[:n-1])
		}

	}
}

func taskHandler(taskPort string, localPort int) {
	dialer := net.Dialer{Timeout: 10 * time.Second}

	serverConn, err := dialer.Dial("tcp", fmt.Sprintf("%s:%s", config.ServerIp, taskPort))
	if err != nil {
		logrus.Errorf("任务连接服务器失败: %v", err)
		return
	}

	localConn, err := dialer.Dial("tcp", fmt.Sprintf(":%d", localPort))
	if err != nil {
		logrus.Errorf("任务连接本地服务失败: %v", err)
		if strings.Contains(err.Error(), "No connection") {
			_, _ = serverConn.Write(common.LOCAL_SERVICE_ERROR)
			_ = serverConn.Close()
			return
		}
		_ = serverConn.Close()
		return
	}

	go common.Transform(localConn, serverConn, common.TransformConfig{
		BufferSize:           config.BufferSize * 1024,
		ConnectionTimeout:    config.ConnectionTimeout,
		DstName:              "local",
		EnableLimit:          config.EnableLimit,
		EnableLongConnection: config.EnableLongConnection,
		LimitBufferSize:      config.LimitBufferSize * 1024,
		SrcName:              "server",
	})

}
