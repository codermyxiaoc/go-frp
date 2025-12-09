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
		go clientConnectLoop(connect, &wg)
	}
	wg.Wait()
}

func clientConnectLoop(connect *common.Connection, mainWg *sync.WaitGroup) {
	defer mainWg.Done()

	delay := 1 * time.Second
	maxDelay := 30 * time.Second

	connInfo := fmt.Sprintf("本地服务地址: %s:%d", connect.LocalHost, connect.LocalPort)

	for {
		logrus.Infof("[%s] 尝试连接服务器: %s:%s", connInfo, config.ServerIp, config.MainPort)

		shouldStop, err := initServer(connect)
		if shouldStop {
			logrus.Errorf("[%s] 配置或密钥错误，停止重试: %v", connInfo, err)
			return
		}
		if err != nil {
			logrus.Errorf("[%s] 主连接握手失败，将在 %v 后重试: %v", connInfo, delay, err)

			time.Sleep(delay)
			delay *= 2
			if delay > maxDelay {
				delay = maxDelay
			}
			continue
		}

		delay = 1 * time.Second

		logrus.Infof("[%s] 握手成功，启动任务监听连接: %s:%d", connInfo, config.ServerIp, connect.TaskPort)

		err = startServer(connect)

		if err != nil {
			logrus.Errorf("[%s] 主连接意外断开，将在 %v 后重试: %v", connInfo, delay, err)
			time.Sleep(delay)
			delay *= 2
			if delay > maxDelay {
				delay = maxDelay
			}
			continue
		}
	}
}

func initServer(connect *common.Connection) (bool, error) {
	mainConn, err := net.Dial("tcp", fmt.Sprintf("%s:%s", config.ServerIp, config.MainPort))
	if err != nil {
		return false, err
	}
	defer func() { _ = mainConn.Close() }()

	connectByte, err := json.Marshal(connect)
	if err != nil {
		logrus.Errorf("序列化连接配置失败: %v", err)
		return true, err
	}
	_, err = mainConn.Write(append(connectByte, common.DELIM))
	if err != nil {
		return false, err
	}

	reader := bufio.NewReader(mainConn)
	masterPort, err := reader.ReadString(common.DELIM)
	if err != nil {
		return false, err
	}
	masterPort = strings.TrimSpace(masterPort)

	if masterPort == common.SECRET_ERROR {
		return true, errors.New(fmt.Sprintf("密钥错误[%s]", connect.Secret))
	}
	if masterPort == common.TASK_ERROR {
		return true, errors.New(fmt.Sprintf("task端口占用[%d]", connect.TaskPort))
	}

	if connect.TaskPort == 0 {
		if connect.TaskPort, err = strconv.Atoi(masterPort); err != nil {
			logrus.Errorf("解析task端口失败: %v", err)
			return true, err
		}
	}
	return false, nil
}

func startServer(connect *common.Connection) error {
	masterConn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", config.ServerIp, connect.TaskPort), 10*time.Second)
	if err != nil {
		return err
	}

	defer func() {
		_ = masterConn.Close()
	}()

	_, err = masterConn.Write(append([]byte(strconv.Itoa(connect.WebPort)), common.DELIM))
	if err != nil {
		logrus.Errorf("发送Web端口失败: %v", err)
		return err
	}

	var wg sync.WaitGroup
	wg.Add(2)

	go keepAlive(masterConn, &wg)
	go listenNotify(masterConn, connect, &wg)

	wg.Wait()

	return errors.New("任务连接已断开")
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
				logrus.Errorf("发送心跳包失败，连接断开: %v", err)
				return
			}
		}
	}
}

func listenNotify(masterConn net.Conn, connect *common.Connection, wg *sync.WaitGroup) {
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
				logrus.Debugf("读取超时，继续等待数据")
				continue
			}
			logrus.Errorf("读取数据失败，连接断开: %v", err)
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
			go taskHandler(connect)
			continue
		case readString[:n-1] == "0":
			logrus.Errorf("服务端web端口[%d]被占用, 停止重试", connect.WebPort)
			_ = masterConn.Close()
			return
		case readString[:1] == ":":
			if connect.WebPort == 0 {
				if connect.WebPort, err = strconv.Atoi(readString[1 : len(readString)-1]); err != nil {
					logrus.Errorf("解析web端口失败: %v", err)
					_ = masterConn.Close()
					return
				}
			}
			logrus.Printf("%s <-> %s 本地端口: %s:%d web访问地址: http://%s:%d", masterConn.RemoteAddr(), masterConn.LocalAddr(), connect.LocalHost, connect.LocalPort, config.ServerIp, connect.WebPort)
		}
	}
}

func taskHandler(connect *common.Connection) {
	serverConn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", config.ServerIp, connect.TaskPort), 10*time.Second)
	if err != nil {
		logrus.Errorf("任务连接服务器失败: %v", err)
		return
	}

	localConn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", connect.LocalHost, connect.LocalPort), 10*time.Second)
	if err != nil {
		logrus.Errorf("任务连接本地服务失败: %v", err)
		if strings.Contains(err.Error(), "connection refused") || strings.Contains(err.Error(), "No connection") {
			_, _ = serverConn.Write(common.LOCAL_SERVICE_ERROR)
			_ = serverConn.Close()
			return
		}
		_ = serverConn.Close()
		return
	}

	go common.Transform(localConn, serverConn, common.TransformConfig{
		ConnectionTimeout:    config.ConnectionTimeout,
		DstName:              "local",
		EnableLimit:          config.EnableLimit,
		EnableLongConnection: config.EnableLongConnection,
		LimitBufferSize:      config.LimitBufferSize * 1024,
		SrcName:              "server",
	})
}
