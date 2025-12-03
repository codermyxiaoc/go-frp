package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/sirupsen/logrus"
	"github.com/spf13/viper"
	"go-frp/common"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Config struct {
	ServerIp             string               `mapstructure:"server-ip" json:"serverIp"`
	BufferSize           int                  `mapstructure:"buffer-size" json:"bufferSize"`
	KeepAliveTime        int                  `mapstructure:"keep-alive-time" json:"keepAliveTime"`
	ConnectionTimeout    int64                `mapstructure:"connection-timeout" json:"connectionTimeout"`
	EnableLongConnection bool                 `mapstructure:"enable-long-connection" json:"enableLongConnection"`
	Secret               string               `mapstructure:"secret" json:"secret"`
	MainPort             string               `mapstructure:"main-port" json:"mainPort"`
	EnableLimit          bool                 `mapstructure:"enable-limit" json:"enableLimit"`
	LimitBufferSize      int                  `mapstructure:"limit-buffer-size" json:"limitBufferSize"`
	Connections          []*common.Connection `mapstructure:"connections" json:"connections"`
}

var config Config

func init() {
	common.InitLog()
	v := viper.New()
	v.SetConfigName("config")
	v.AddConfigPath(".")
	v.SetConfigType("yaml")

	v.SetDefault("server-ip", "127.0.0.1")
	v.SetDefault("buffer-size", 512)
	v.SetDefault("keep-alive-time", 10)
	v.SetDefault("connection-timeout", 30)
	v.SetDefault("enable-long-connection", false)
	v.SetDefault("secret", "secret")
	v.SetDefault("main-port", "12345")
	v.SetDefault("enable-limit", false)
	v.SetDefault("limit-buffer-size", 1024)

	if err := v.ReadInConfig(); err != nil {
		log.Printf("读取配置文件失败: %v", err)
	}
	if err := v.Unmarshal(&config); err != nil {
		log.Printf("解析配置文件失败: %v", err)
	}
}

func main() {
	if len(config.Connections) <= 0 {
		logrus.Errorf("请配置内网穿透端口映射:[connections]")
		return
	}
	var wg sync.WaitGroup
	wg.Add(len(config.Connections))
	for _, connect := range config.Connections {
		if connect.Secret == "" {
			connect.Secret = config.Secret
		}
		go initClient(connect, &wg)
	}
	wg.Wait()
}

func initClient(connect *common.Connection, mainWg *sync.WaitGroup) {
	defer mainWg.Done()

	mainConn, err := net.Dial("tcp", fmt.Sprintf("%s:%s", config.ServerIp, config.MainPort))
	if err != nil {
		logrus.Errorf("连接服务器主连接失败: %v", err)
		return
	}
	defer func() { _ = mainConn.Close() }()

	connectJson, err := json.Marshal(connect)
	if err != nil {
		logrus.Errorf("序列化连接配置失败: %v", err)
		return
	}
	_, err = mainConn.Write(append(connectJson, common.DELIM))
	if err != nil {
		logrus.Errorf("发送连接配置失败: %v", err)
		return
	}

	reader := bufio.NewReader(mainConn)
	masterPort, err := reader.ReadString(common.DELIM)
	if err != nil {
		logrus.Errorf("读取服务器端口失败: %v", err)
		return
	}
	masterPort = masterPort[:len(masterPort)-1]
	if masterPort == "00000" {
		logrus.Errorf("密钥错误[%s]", config.Secret)
		return
	}
	if masterPort == "100000" {
		logrus.Errorf("task端口占用[%d]", connect.TaskPort)
		return
	}
	if connect.TaskPort == 0 {
		if connect.TaskPort, err = strconv.Atoi(masterPort); err != nil {
			logrus.Errorf("解析task端口失败: %v", err)
			return
		}
	}
	_ = mainConn.Close()
	startServer(connect)
}

func startServer(connect *common.Connection) {
	dialer := net.Dialer{Timeout: 10 * time.Second}
	masterConn, err := dialer.Dial("tcp", fmt.Sprintf("%s:%d", config.ServerIp, connect.TaskPort))
	if err != nil {
		logrus.Errorf("连接服务器失败: %v", err)
		return
	}

	_, err = masterConn.Write(append([]byte(strconv.Itoa(connect.WebPort)), common.DELIM))
	if err != nil {
		logrus.Errorf("发送Web端口失败: %v", err)
		return
	}

	var wg sync.WaitGroup
	wg.Add(2)

	go keepAlive(masterConn, &wg)
	go inform(masterConn, connect, &wg)

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

func inform(masterConn net.Conn, connect *common.Connection, wg *sync.WaitGroup) {
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
			go taskHandler(connect.TaskPort, connect.LocalPort)
			continue
		case readString[:n-1] == "0":
			_ = masterConn.Close()
			logrus.Errorf("服务端web端口[%d]被占用", connect.WebPort)
			return
		case readString[:1] == ":":
			if connect.WebPort == 0 {
				if connect.WebPort, err = strconv.Atoi(readString[1 : len(readString)-1]); err != nil {
					logrus.Errorf("解析web端口失败: %v", err)
					_ = masterConn.Close()
					return
				}
			}
			logrus.Printf("%s <-> %s 本地端口: %d web访问地址: http://%s:%d", masterConn.RemoteAddr(), masterConn.LocalAddr(), connect.LocalPort, config.ServerIp, connect.WebPort)
		}

	}
}

func taskHandler(taskPort int, localPort int) {
	dialer := net.Dialer{Timeout: 10 * time.Second}

	serverConn, err := dialer.Dial("tcp", fmt.Sprintf("%s:%d", config.ServerIp, taskPort))
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
