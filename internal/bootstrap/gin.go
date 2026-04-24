package bootstrap

import (
	"github.com/gin-gonic/gin"
)

// trustedProxyCIDRs 信任的内网代理网段。
// 生产拓扑：Cloudflare → 阿里云 ALB → K8s Ingress(ALB/Nginx) → Pod。
// 从 Pod 视角看，remote addr 永远是 K8s 集群内网 IP（ALB/Ingress 的 Pod ENI）。
// 只有来自这些内网段的请求，其 X-Forwarded-For / CF-Connecting-IP 头才被信任。
// 本地开发 docker-compose 用 172.17/172.18/127 回环。
var trustedProxyCIDRs = []string{
	"127.0.0.1/32",
	"::1/128",
	"10.0.0.0/8",     // K8s Pod/Service 内网
	"172.16.0.0/12",  // Docker 默认网桥 + K8s 常用段
	"192.168.0.0/16", // VPC/私网
	"100.64.0.0/10",  // K8s CGNAT（部分云厂商使用）
	"fd00::/8",       // IPv6 ULA
}

// ConfigureTrustedProxies 为 Gin 引擎配置代理信任策略：
//  1. SetTrustedProxies 白名单：只有这些网段发来的 X-Forwarded-For 才采信
//  2. TrustedPlatform = "CF-Connecting-IP"：Cloudflare 把真实客户端 IP 放在此头
//     Gin 的 c.ClientIP() 会优先读它，跳过 X-Forwarded-For 解析
//
// 所有入口 (server / gateway / backend / worker / monolith) 创建 engine 后必须调用。
func ConfigureTrustedProxies(engine *gin.Engine) {
	_ = engine.SetTrustedProxies(trustedProxyCIDRs)
	engine.TrustedPlatform = "CF-Connecting-IP"
}
