package main

import (
	"bufio"
	"context"
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
	ConnChanCount        int    `mapstructure:"conn-chan-count" json:"connChanCount"`
	EnableLongConnection bool   `mapstructure:"enable-long-connection" json:"enableLongConnection"`
	ConnectionTimeout    int64  `mapstructure:"connection-timeout" json:"connectionTimeout"`
	KeepAliveTime        int    `mapstructure:"keep-alive-time" json:"keepAliveTime"`
	Secret               string `mapstructure:"secret" json:"secret"`
	MainPort             string `mapstructure:"main-port" json:"mainPort"`
	EnableLimit          bool   `mapstructure:"enable-limit" json:"enableLimit" json:"enableLimit"`
	LimitBufferSize      int    `mapstructure:"limit-buffer-size" json:"limitBufferSize" json:"limitBufferSize"`
}

var config Config

func init() {
	common.InitLog()
	v := viper.New()
	v.SetConfigName("config")
	v.AddConfigPath(".")
	v.SetConfigType("yaml")

	v.SetDefault("conn-chan-count", 200)
	v.SetDefault("enable-long-connection", false)
	v.SetDefault("connection-timeout", 30)
	v.SetDefault("keep-alive-time", 10)
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
	mainListen, err := net.Listen("tcp", fmt.Sprintf(":%s", config.MainPort))
	if err != nil {
		logrus.Errorf("主服务器监听失败: %v", err)
		return
	}
	defer func() { _ = mainListen.Close() }()

	for {
		mainConn, err := mainListen.Accept()
		if err != nil {
			logrus.Errorf("客户端连接主服务失败: %v", err)
			continue
		}
		go initServer(mainConn)
	}
}

func initServer(mainConn net.Conn) {
	defer func() {
		err := recover()
		if err != nil {
			logrus.Errorf("panic: %v", err)
			_ = mainConn.Close()
		}
	}()

	err := mainConn.SetReadDeadline(time.Now().Add(10 * time.Second))
	if err != nil {
		logrus.Errorf("设置main连接读取超时失败: %v", err)
		_ = mainConn.Close()
		return
	}
	reader := bufio.NewReader(mainConn)
	connectByte, err := reader.ReadBytes(common.DELIM)
	if err != nil {
		logrus.Errorf("读取客户端连接配置失败: %v", err)
		_ = mainConn.Close()
		return
	}
	var connect common.Connection
	err = json.Unmarshal(connectByte, &connect)
	if err != nil {
		logrus.Errorf("反序列化连接配置失败: %v", err)
		_ = mainConn.Close()
		return
	}
	if len(config.Secret) != len(connect.Secret) || connect.Secret != config.Secret {
		logrus.Errorf("密钥错误:[%s](%s)", connect.Secret, mainConn.RemoteAddr().String())
		_, _ = mainConn.Write(append([]byte(common.SECRET_ERROR), common.DELIM))
		_ = mainConn.Close()
		return
	}
	err = mainConn.SetDeadline(time.Time{})
	if err != nil {
		logrus.Errorf("重置main连接读取超时失败: %v", err)
		_ = mainConn.Close()
		return
	}

	go startServer(mainConn, &connect)
}

func startServer(mainConn net.Conn, connect *common.Connection) {
	exitSignal := make(chan struct{})

	masterListen, err := net.Listen("tcp", fmt.Sprintf(":%d", connect.TaskPort))
	if err != nil {
		logrus.Errorf("master连接监听启动失败: %v", err)
		if strings.Contains(err.Error(), "bind: Only one usage of each socket address") {
			_, _ = mainConn.Write(append([]byte(common.TASK_ERROR), common.DELIM))
		}
		_ = mainConn.Close()
		return
	}

	port := masterListen.Addr().(*net.TCPAddr).Port
	_, err = mainConn.Write(append([]byte(strconv.Itoa(port)), common.DELIM))
	if err != nil {
		logrus.Errorf("发送master连接端口失败指令失败（%s）: %v", mainConn.RemoteAddr(), err)
		_ = mainConn.Close()
		_ = masterListen.Close()
		return
	}
	_ = mainConn.Close()
	// TODO master如果等不到第一条连接会卡死在这里
	masterConn, err := masterListen.Accept()
	if err != nil {
		logrus.Errorf("主连接失败: %v", err)
		_ = masterListen.Close()
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	informChan := make(chan struct{}, config.ConnChanCount)
	connChan := make(chan net.Conn, config.ConnChanCount)
	var wg sync.WaitGroup

	wg.Add(3)
	go listenNotify(masterConn, informChan, exitSignal, &wg)
	go listenWebConnect(connChan, informChan, ctx, masterConn, connect.WebPort, exitSignal, &wg)
	go listenTaskConnect(masterListen, connChan, &wg)

	logrus.Printf("%s<->%s 转发接口启动成功", masterConn.LocalAddr(), masterConn.RemoteAddr())
	select {
	case <-exitSignal:
		cancel()
		_ = masterListen.Close()
		_ = masterConn.Close()
		wg.Wait()
		close(informChan)
		close(connChan)
		logrus.Printf("%s<->%s 转发接口退出成功", masterConn.LocalAddr(), masterConn.RemoteAddr())
		return
	}
}

func listenNotify(masterConn net.Conn, informChan <-chan struct{}, exitSignal chan<- struct{}, wg *sync.WaitGroup) {
	defer func() {
		wg.Done()
		exitSignal <- struct{}{}
	}()

	ticker := time.NewTicker(time.Duration(config.KeepAliveTime) * time.Second)
	defer ticker.Stop()
	remoteAddr := masterConn.RemoteAddr().String()
	for {
		select {
		case _, ok := <-informChan:
			if !ok {
				return
			}
			_, err := masterConn.Write([]byte(common.NEW_TASK))
			if err != nil {
				logrus.Errorf("发送new指令失败（%s）: %v", remoteAddr, err)
				return
			}

		case <-ticker.C:
			_, err := masterConn.Write([]byte(common.PI))
			if err != nil {
				logrus.Errorf("发送心跳包失败（%s）: %v", remoteAddr, err)
				return
			}
		}
	}
}

func listenWebConnect(connChan chan<- net.Conn, informChan chan<- struct{}, ctx context.Context, masterConn net.Conn, webPort int, exitSignal chan<- struct{}, wg *sync.WaitGroup) {
	defer func() {
		wg.Done()
	}()
	webListen, err := net.Listen("tcp", fmt.Sprintf(":%d", webPort))
	if err != nil {
		logrus.Errorf("web监听启动失败: %v", err)
		if strings.Contains(err.Error(), "bind: Only one usage of each socket address") {
			_, _ = masterConn.Write([]byte(fmt.Sprintf("%d%c", 0, common.DELIM)))
		}
		exitSignal <- struct{}{}
		return
	}
	go func() {
		<-ctx.Done()
		_ = webListen.Close()
	}()

	_, err = masterConn.Write([]byte(fmt.Sprintf(":%d%c", webListen.Addr().(*net.TCPAddr).Port, common.DELIM)))
	if err != nil {
		logrus.Errorf("发送web端口失败指令失败（%s）: %v", masterConn.RemoteAddr(), err)
		_ = masterConn.Close()
		return
	}

	for {
		webConn, err := webListen.Accept()
		if err != nil {
			logrus.Errorf("web端接收连接失败: %v", err)
			var opErr *net.OpError
			if errors.As(err, &opErr) && opErr.Op == "accept" && opErr.Err.Error() == "use of closed network connection" {
				return
			}
			continue
		}

		go func() {
			select {
			case connChan <- webConn:
				select {
				case informChan <- struct{}{}:
				default:
					logrus.Warning("消息通道已满，无法通知新任务连接")
					_, _ = webConn.Write(common.WEB_CHAN_ERROR)
					_ = webConn.Close()
				}
			default:
				logrus.Warning("连接通道已满，无法保存web连接")
				_, _ = webConn.Write(common.WEB_CHAN_ERROR)
				_ = webConn.Close()
			}
		}()
	}
}

func listenTaskConnect(masterListen net.Listener, connChan <-chan net.Conn, wg *sync.WaitGroup) {
	defer func() {
		wg.Done()
	}()

	for {
		taskConn, err := masterListen.Accept()
		if err != nil {
			logrus.Errorf("接收任务连接失败: %v", err)
			var opErr *net.OpError
			if errors.As(err, &opErr) && opErr.Op == "accept" && opErr.Err.Error() == "use of closed network connection" {
				return
			}
			continue
		}

		go func(taskConn net.Conn) {
			select {
			case webConn, ok := <-connChan:
				if !ok {
					logrus.Error("通获取web连接错误，关闭任务连接")
					_ = taskConn.Close()
					return
				}
				go common.Transform(taskConn, webConn, common.TransformConfig{
					ConnectionTimeout:    config.ConnectionTimeout,
					DstName:              "task",
					EnableLimit:          config.EnableLimit,
					EnableLongConnection: config.EnableLongConnection,
					LimitBufferSize:      config.LimitBufferSize * 1024,
					SrcName:              "web",
				})
			case <-time.After(3 * time.Second):
				logrus.Warning("任务连接10秒内无web连接配对，已关闭")
				_ = taskConn.Close()
			}
		}(taskConn)
	}
}
