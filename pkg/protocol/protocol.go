package protocol

// ServerAddr is the address of the remote server.
// Clients will send ICMP packets to this address.
// 注意：这里应该填写你服务器的公网 IP 地址。
const ServerAddr = "127.0.0.1" // 请将这里修改为你服务器的公网 IP

// LocalProxyAddr is the address the client will listen on to act as an HTTP proxy.
const LocalProxyAddr = "localhost:8888"
