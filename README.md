# golang内网穿透

- 多协程与通道配合优化
- 心跳包维护主连接
- 服务端优雅重连
- 主连接+任务连接保证大文件穿透稳定传输
- 使用缓冲区进行读写交换
- yml配置 手动配置服务各个参数（缓冲区大小，通道大小，心跳包时间等）
- 长连接超时策略

## 说明
- server端： 具有公网地址的服务器
- client端： 需要内网穿透的主机

## 使用

server端, web服务默认端口8088，client服务接口8080
```bash
go run server.go
```

client端 默认映射8090的服务
```bash
go run client.go
```

## 配置

<div align="center">
  <img src="./images/wechat_2025-10-06_104926_336.png" width="800">
  <br>
</div>


```yml
#commion

#缓冲区大小
buffer-size: 5 #mb
#心跳包速度
keep-alive-time: 10 #s
#服务端端口
server-port: 8080
#长连接超时时间
idle-timeout: 30 #s

#server
#server 的web端端口
web-port: 8088
#接收任务请求的通道大小
conn-chan-count: 100

#client
#服务端ip
server-ip: 127.0.0.1
#本地服务端口
local-port: 8090
```

## 测试截图

并发测试 1秒500线程10次循环 全部成功 其他暂时还未测试

<div align="center">
  <img src="./images/wechat_2025-10-06_114438_201.png" width="800">
  <br>
</div>

<div align="center">
  <img src="./images/wechat_2025-10-06_114625_143.png" width="800">
  <br>
</div>

大文件下载测试


<div align="center">
  <img src="./images/wechat_2025-10-04_234208_609.png" width="800">
  <br>
</div>

## 代码服务器截图

运行截图


<div align="center">
  <img src="./images/wechat_2025-10-05_031410_348.png" width="800">
  <br>
</div>

<div align="center">
  <img src="./images/wechat_2025-10-05_031403_139.png" width="800">
  <br>
</div>

<div align="center">
  <img src="./images/wechat_2025-10-05_031441_351.png" width="800">
  <br>
</div>

<div align="center">
  <img src="./images/wechat_2025-10-05_031617_055.png" width="800">
  <br>
</div>