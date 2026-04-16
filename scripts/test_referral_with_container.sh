#!/bin/bash

# 邀请返佣系统测试脚本 - 使用临时测试容器
# 用途：在包含 Go 工具链的临时容器中运行测试

set -e

echo "=========================================="
echo "邀请返佣系统全流程测试"
echo "=========================================="
echo ""

# 检查数据库连接
echo "1. 检查测试数据库连接..."
if ! docker compose exec -T mysql mysql -uroot -proot123456 -e "USE tokenhubhk; SELECT 1;" > /dev/null 2>&1; then
    echo "❌ 数据库连接失败，请确保 Docker Compose 服务已启动"
    exit 1
fi
echo "✅ 数据库连接正常"
echo ""

# 获取绝对路径（处理 Windows Git Bash 路径转换）
BACKEND_PATH="$(cd backend && pwd -W 2>/dev/null || pwd)"
echo "2. 后端代码路径: ${BACKEND_PATH}"
echo ""

# 获取数据库连接信息
TEST_DSN="root:root123456@tcp(host.docker.internal:3306)/tokenhubhk?charset=utf8mb4&parseTime=True&loc=Local"
echo "3. 使用测试 DSN: ${TEST_DSN}"
echo ""

# 创建临时测试容器并运行测试
echo "4. 启动临时测试容器..."
docker run --rm \
    --network host \
    --add-host=host.docker.internal:host-gateway \
    -v "${BACKEND_PATH}:/app" \
    -w /app \
    -e TEST_DATABASE_DSN="${TEST_DSN}" \
    -e GOPROXY="https://goproxy.cn,https://goproxy.io,direct" \
    golang:1.23-alpine \
    sh -c '
        echo "=== 安装依赖 ==="
        apk add --no-cache git ca-certificates tzdata

        echo ""
        echo "=== 下载 Go 模块 ==="
        go mod download

        echo ""
        echo "=========================================="
        echo "开始运行测试"
        echo "=========================================="
        echo ""

        # 单元测试：邀请服务基础功能
        echo "【单元测试】邀请服务基础功能"
        echo "----------------------------------------"
        go test -v ./internal/service/referral/ -run "^TestReferralCodeGeneration_Uniqueness$" 2>&1 | grep -E "PASS|FAIL|RUN|---" || true
        go test -v ./internal/service/referral/ -run "^TestGetOrCreateLink_Idempotent$" 2>&1 | grep -E "PASS|FAIL|RUN|---" || true
        echo ""

        # 单元测试：归因解锁
        echo "【单元测试】归因解锁逻辑"
        echo "----------------------------------------"
        go test -v ./internal/service/referral/ -run "^TestTryUnlockAttribution_BelowThreshold$" 2>&1 | grep -E "PASS|FAIL|RUN|---" || true
        echo ""

        # 集成测试：完整流程
        echo "【集成测试】完整业务流程"
        echo "----------------------------------------"
        go test -v ./internal/service/referral/ -run "^TestReferralFlow_E2E_Complete$" 2>&1 | grep -E "PASS|FAIL|RUN|---" || true
        echo ""

        # 配置动态生效测试
        echo "【配置测试】动态生效验证"
        echo "----------------------------------------"
        go test -v ./internal/service/referral/ -run "^TestConfigDynamicUpdate_CommissionRateChange$" 2>&1 | grep -E "PASS|FAIL|RUN|---" || true
        echo ""

        # API 测试
        echo "【API 测试】公开配置端点"
        echo "----------------------------------------"
        go test -v ./internal/handler/public/ -run "^TestGetReferralConfig_Success$" 2>&1 | grep -E "PASS|FAIL|RUN|---" || true
        echo ""

        echo "=========================================="
        echo "测试执行完成"
        echo "=========================================="
    '

echo ""
echo "✅ 测试运行完成"
echo ""
