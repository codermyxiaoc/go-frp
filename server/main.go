package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"github.com/sirupsen/logrus"
	"github.com/spf13/viper"
	"go-frp/common"
	"log"
	"net"
	"strconv"
	"sync"
	"time"
)

type Config struct {
	ConnChanCount        int    `mapstructure:"conn-chan-count" json:"connChanCount"`
	EnableLongConnection bool   `mapstructure:"enableLong-connection" json:"enableLongConnection"`
	ConnectionTimeout    int64  `mapstructure:"connection-timeout" json:"connectionTimeout"`
	BufferSize           int    `mapstructure:"buffer-size" json:"bufferSize"`
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
	v.SetDefault("buffer-size", 5)
	v.SetDefault("connection-timeout", 30)
	v.SetDefault("conn-chan-count", 100)
	v.SetDefault("keep-alive-time", 10)
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
	mainListen, err := net.Listen("tcp", fmt.Sprintf(":%s", config.MainPort))
	defer func() { _ = mainListen.Close() }()
	if err != nil {
		logrus.Errorf("主服务器监听失败: %v", err)
		return
	}
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
			log.Println(err)
			_ = mainConn.Close()
		}
	}()
	err := mainConn.SetReadDeadline(time.Now().Add(10 * time.Second))
	if err != nil {
		logrus.Errorf("设置主连接超时失败: %v", err)
		return
	}
	reader := bufio.NewReader(mainConn)
	clientSecret, err := reader.ReadString(common.DELIM)
	if err != nil {
		logrus.Errorf("读取客户端密钥失败: %v", err)
		return
	}
	if len(config.Secret) != (len(clientSecret)-1) || clientSecret[0:len(config.Secret)] != config.Secret {
		_, _ = mainConn.Write([]byte("00000"))
		logrus.Errorf("密钥错误[%s]（%s）", clientSecret, mainConn.RemoteAddr().String())
		return
	}
	err = mainConn.SetDeadline(time.Time{})
	if err != nil {
		logrus.Errorf("重置main连接超时失败: %v", err)
		return
	}
	go startService(mainConn)
}

func startService(mainConn net.Conn) {
	exitChan := make(chan struct{})

	masterListen, err := net.Listen("tcp", ":0")
	if err != nil {
		logrus.Errorf("master连接监听启动失败: %v", err)
		_ = mainConn.Close()
		return
	}
	port := masterListen.Addr().(*net.TCPAddr).Port

	_, err = mainConn.Write([]byte(strconv.Itoa(port)))
	if err != nil {
		logrus.Errorf("发送master连接端口失败指令失败（%s）: %v", mainConn.RemoteAddr(), err)
		_ = mainConn.Close()
		_ = masterListen.Close()
		return
	}
	_ = mainConn.Close()

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
	go inform(masterConn, informChan, exitChan, &wg)
	go acceptWeb(connChan, informChan, ctx, masterConn, &wg)
	go acceptTask(masterListen, connChan, &wg)

	logrus.Printf("%s<->%s 转发接口启动成功", masterConn.LocalAddr(), masterConn.RemoteAddr())
	select {
	case <-exitChan:
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

func inform(masterConn net.Conn, informChan <-chan struct{}, exitChan chan<- struct{}, wg *sync.WaitGroup) {
	defer func() {
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

func acceptWeb(connChan chan<- net.Conn, informChan chan<- struct{}, ctx context.Context, masterConn net.Conn, wg *sync.WaitGroup) {
	defer func() {
		wg.Done()
	}()

	webListen, err := net.Listen("tcp", fmt.Sprintf(":0"))
	if err != nil {
		logrus.Errorf("web监听启动失败: %v", err)
		return
	}
	go func() {
		<-ctx.Done()
		_ = webListen.Close()
	}()

	webPort := webListen.Addr().(*net.TCPAddr).Port
	_, err = masterConn.Write([]byte(fmt.Sprintf(":%d%c", webPort, common.DELIM)))
	if err != nil {
		logrus.Errorf("发送web端口失败指令失败（%s）: %v", masterConn.RemoteAddr(), err)
		_ = masterConn.Close()
		return
	}

	for {
		webConn, err := webListen.Accept()
		if err != nil {
			var opErr *net.OpError
			if errors.As(err, &opErr) && opErr.Op == "accept" && opErr.Err.Error() == "use of closed network connection" {
				return
			}
			logrus.Errorf("web端接收连接失败: %v", err)
			continue
		}

		webAddr := webConn.RemoteAddr().String()

		go func() {
			select {
			case connChan <- webConn:
				select {
				case informChan <- struct{}{}:
				default:
					logrus.Warningf("informChan 已满，无法通知新web连接（%s）", webAddr)
					_ = webConn.Close()
				}
			default:
				logrus.Warningf("connChan 已满，关闭新web连接（%s）", webAddr)
				_ = webConn.Close()
			}
		}()
	}
}

func acceptTask(masterListen net.Listener, connChan <-chan net.Conn, wg *sync.WaitGroup) {
	defer func() {
		wg.Done()
	}()

	for {
		taskConn, err := masterListen.Accept()
		if err != nil {
			var opErr *net.OpError
			if errors.As(err, &opErr) && opErr.Op == "accept" && opErr.Err.Error() == "use of closed network connection" {
				return
			}
			logrus.Errorf("接收任务连接失败: %v", err)
			continue
		}
		taskAddr := taskConn.RemoteAddr().String()

		go func(taskConn net.Conn) {
			select {
			case webConn, ok := <-connChan:
				if !ok {
					logrus.Errorf("connChan 已关闭，关闭任务连接（%s）", taskAddr)
					_ = taskConn.Close()
					return
				}
				go common.Transform(taskConn, webConn, common.TransformConfig{
					BufferSize:           config.BufferSize * 1024,
					ConnectionTimeout:    config.ConnectionTimeout,
					DstName:              "task",
					EnableLimit:          config.EnableLimit,
					EnableLongConnection: config.EnableLongConnection,
					LimitBufferSize:      config.LimitBufferSize * 1024,
					SrcName:              "web",
				})
			case <-time.After(10 * time.Second):
				logrus.Warningf("任务连接（%s）10秒内无web连接配对，已关闭", taskAddr)
				_ = taskConn.Close()
			}
		}(taskConn)
	}
}
